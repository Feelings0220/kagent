from __future__ import annotations

import logging
import tempfile
from pathlib import Path
from typing import Optional

from google.adk.agents import BaseAgent, LlmAgent

from ..tools import BashTool, EditFileTool, ReadFileTool, WriteFileTool
from .skill_tool import SkillsTool

logger = logging.getLogger("kagent_adk." + __name__)


def add_builtin_tools_to_agent(
    agent: BaseAgent,
    names: list[str],
    workspace_directory: str | Path | None = None,
) -> None:
    """Add built-in workspace tools (bash, read_file, write_file, edit_file)
    requested via the agent config, without requiring skills to be configured.

    Args:
      agent: The LlmAgent instance to which the tools will be added.
      names: Built-in tool names to enable. Unknown names are ignored so a
        newer CRD enum value doesn't break an older runtime.
      workspace_directory: Root directory for the tools. Defaults to
        <tempdir>/kagent-workspace; created when missing.
    """
    if not isinstance(agent, LlmAgent) or not names:
        return

    workspace = (
        Path(workspace_directory) if workspace_directory else Path(tempfile.gettempdir()) / "kagent-workspace"
    )
    workspace.mkdir(parents=True, exist_ok=True)

    existing_tool_names = {getattr(t, "name", None) for t in agent.tools}
    requested = set(names)

    if "bash" in requested and "bash" not in existing_tool_names:
        agent.tools.append(BashTool(workspace))
        logger.debug(f"Added builtin bash tool to agent: {agent.name}")

    if "read_file" in requested and "read_file" not in existing_tool_names:
        agent.tools.append(ReadFileTool(workspace))
        logger.debug(f"Added builtin read file tool to agent: {agent.name}")

    if "write_file" in requested and "write_file" not in existing_tool_names:
        agent.tools.append(WriteFileTool())
        logger.debug(f"Added builtin write file tool to agent: {agent.name}")

    if "edit_file" in requested and "edit_file" not in existing_tool_names:
        agent.tools.append(EditFileTool())
        logger.debug(f"Added builtin edit file tool to agent: {agent.name}")


def add_skills_tool_to_agent(
    skills_directory: str | Path,
    agent: BaseAgent,
) -> None:
    """Utility function to add Skills and Bash tools to a given agent.

    Args:
      agent: The LlmAgent instance to which the tools will be added.
      skills_directory: Path to directory containing skill folders.
    """

    if not isinstance(agent, LlmAgent):
        return

    skills_directory = Path(skills_directory)
    existing_tool_names = {getattr(t, "name", None) for t in agent.tools}

    # Add SkillsTool if not already present
    if "skills" not in existing_tool_names:
        agent.tools.append(SkillsTool(skills_directory))
        logger.debug(f"Added skills invoke tool to agent: {agent.name}")

    # Add BashTool if not already present
    if "bash" not in existing_tool_names:
        agent.tools.append(BashTool(skills_directory))
        logger.debug(f"Added bash tool to agent: {agent.name}")

    if "read_file" not in existing_tool_names:
        agent.tools.append(ReadFileTool(skills_directory))
        logger.debug(f"Added read file tool to agent: {agent.name}")

    if "write_file" not in existing_tool_names:
        agent.tools.append(WriteFileTool())
        logger.debug(f"Added write file tool to agent: {agent.name}")

    if "edit_file" not in existing_tool_names:
        agent.tools.append(EditFileTool())
        logger.debug(f"Added edit file tool to agent: {agent.name}")
