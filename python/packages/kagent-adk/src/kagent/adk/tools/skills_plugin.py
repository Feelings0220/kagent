from __future__ import annotations

import logging
import tempfile
from pathlib import Path
from typing import Optional

from google.adk.agents import BaseAgent, LlmAgent

from ..tools import BashTool, EditFileTool, ReadFileTool, WriteFileTool
from .skill_tool import SkillsTool

logger = logging.getLogger("kagent_adk." + __name__)


# Factory for each workspace tool. Tools that operate on the session
# workspace take its directory; write/edit resolve paths per-session at call
# time and take no constructor argument.
def _workspace_tool_factories(workspace: Path):
    return {
        "bash": lambda: BashTool(workspace),
        "read_file": lambda: ReadFileTool(workspace),
        "write_file": lambda: WriteFileTool(),
        "edit_file": lambda: EditFileTool(),
    }


def _append_workspace_tools(agent: LlmAgent, workspace: Path, names) -> None:
    """Append the named workspace tools not already present on the agent."""
    existing_tool_names = {getattr(t, "name", None) for t in agent.tools}
    for name, factory in _workspace_tool_factories(workspace).items():
        if name in names and name not in existing_tool_names:
            agent.tools.append(factory())
            logger.debug(f"Added {name} tool to agent: {agent.name}")


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

    _append_workspace_tools(agent, workspace, set(names))


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

    # Add SkillsTool (discovery) if not already present; the workspace tools
    # below are shared with the builtin-tools path.
    if "skills" not in existing_tool_names:
        agent.tools.append(SkillsTool(skills_directory))
        logger.debug(f"Added skills invoke tool to agent: {agent.name}")

    _append_workspace_tools(agent, skills_directory, {"bash", "read_file", "write_file", "edit_file"})
