"""Tests for builtin workspace tools and the upload materializer plugin."""

import shutil
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest
from google.adk.agents import LlmAgent
from google.genai import types

from kagent.adk._upload_plugin import (
    UploadMaterializerPlugin,
    sanitize_upload_name,
    unique_upload_path,
)
from kagent.adk.tools import add_builtin_tools_to_agent


def make_llm_agent() -> LlmAgent:
    return LlmAgent(name="test_agent", model="gemini-2.0-flash")


class TestAddBuiltinTools:
    def test_adds_requested_tools_only(self, tmp_path: Path):
        agent = make_llm_agent()
        add_builtin_tools_to_agent(agent, ["bash", "read_file"], tmp_path)

        names = {getattr(t, "name", None) for t in agent.tools}
        assert names == {"bash", "read_file"}

    def test_creates_missing_workspace_directory(self, tmp_path: Path):
        workspace = tmp_path / "not-yet-created"
        agent = make_llm_agent()
        add_builtin_tools_to_agent(agent, ["write_file"], workspace)

        assert workspace.exists()
        names = {getattr(t, "name", None) for t in agent.tools}
        assert names == {"write_file"}

    def test_unknown_names_ignored(self, tmp_path: Path):
        agent = make_llm_agent()
        add_builtin_tools_to_agent(agent, ["unknown_tool"], tmp_path)
        assert agent.tools == []

    def test_no_duplicates_when_skills_already_added(self, tmp_path: Path):
        agent = make_llm_agent()
        add_builtin_tools_to_agent(agent, ["bash"], tmp_path)
        add_builtin_tools_to_agent(agent, ["bash", "edit_file"], tmp_path)

        names = [getattr(t, "name", None) for t in agent.tools]
        assert names.count("bash") == 1
        assert "edit_file" in names

    def test_empty_names_is_noop(self, tmp_path: Path):
        agent = make_llm_agent()
        add_builtin_tools_to_agent(agent, [], tmp_path)
        assert agent.tools == []


class TestSanitizeUploadName:
    @pytest.mark.parametrize(
        "raw,index,expected",
        [
            ("data.csv", 0, "data.csv"),
            ("../../etc/passwd", 0, "passwd"),
            ("my file (1).txt", 0, "my_file__1_.txt"),
            (".env", 0, "env"),
            ("", 2, "upload-3"),
            (None, 0, "upload-1"),
        ],
    )
    def test_sanitization(self, raw, index, expected):
        assert sanitize_upload_name(raw, index) == expected


class TestUniqueUploadPath:
    def test_collision_gets_suffix(self, tmp_path: Path):
        first = unique_upload_path(tmp_path, "data.csv")
        assert first.name == "data.csv"
        first.write_text("x")

        second = unique_upload_path(tmp_path, "data.csv")
        assert second.name == "data-1.csv"


def make_invocation_context(session_id: str):
    return SimpleNamespace(session=SimpleNamespace(id=session_id))


@pytest.fixture
def session_workspace():
    """Initialize a session workspace and clean it (and the path cache) up."""
    from kagent.skills import session as session_module

    session_id = "test-upload-plugin-session"
    skills_dir = tempfile.mkdtemp()
    session_path = session_module.initialize_session_path(session_id, skills_dir)
    yield session_id, session_path
    session_module._session_path_cache.pop(session_id, None)
    shutil.rmtree(session_path, ignore_errors=True)
    shutil.rmtree(skills_dir, ignore_errors=True)


class TestUploadMaterializerPlugin:
    @pytest.mark.asyncio
    async def test_writes_file_and_replaces_part(self, session_workspace):
        session_id, session_path = session_workspace
        plugin = UploadMaterializerPlugin()

        content = b"col1,col2\n1,2\n"
        message = types.Content(
            role="user",
            parts=[
                types.Part(text="analyze this"),
                types.Part(
                    inline_data=types.Blob(data=content, mime_type="text/csv", display_name="data.csv")
                ),
            ],
        )

        result = await plugin.on_user_message_callback(
            invocation_context=make_invocation_context(session_id), user_message=message
        )

        assert result is not None
        assert len(result.parts) == 2
        assert "uploads/data.csv" in result.parts[1].text
        saved = session_path / "uploads" / "data.csv"
        assert saved.read_bytes() == content

    @pytest.mark.asyncio
    async def test_keeps_image_inline(self, session_workspace):
        session_id, _ = session_workspace
        plugin = UploadMaterializerPlugin()

        message = types.Content(
            role="user",
            parts=[
                types.Part(
                    inline_data=types.Blob(data=b"\x89PNG", mime_type="image/png", display_name="chart.png")
                ),
            ],
        )

        result = await plugin.on_user_message_callback(
            invocation_context=make_invocation_context(session_id), user_message=message
        )

        assert result is not None
        # note + original inline image
        assert len(result.parts) == 2
        assert result.parts[1].inline_data is not None

    @pytest.mark.asyncio
    async def test_text_only_message_untouched(self):
        plugin = UploadMaterializerPlugin()
        message = types.Content(role="user", parts=[types.Part(text="hello")])

        result = await plugin.on_user_message_callback(
            invocation_context=make_invocation_context("any-session"), user_message=message
        )

        assert result is None
