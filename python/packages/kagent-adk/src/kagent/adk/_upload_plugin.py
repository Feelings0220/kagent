"""Plugin that persists uploaded files into the session workspace.

Mirrors the Go runtime's materializeInboundFiles (go/adk/pkg/a2a/uploads.go):
incoming user messages may carry file parts as inline data (from A2A
FileParts). Non-image files are written to the session uploads/ directory and
replaced with a text note pointing the model at the saved path, so the bash
and file tools can read them. Images are saved as well but stay inline so
vision-capable models still see them.
"""

from __future__ import annotations

import logging
import re
from pathlib import Path
from typing import Optional

from google.adk.agents.invocation_context import InvocationContext
from google.adk.plugins.base_plugin import BasePlugin
from google.genai import types

from kagent.skills import get_session_path

logger = logging.getLogger("kagent_adk." + __name__)

_UNSAFE_FILENAME_CHARS = re.compile(r"[^a-zA-Z0-9._-]")


def sanitize_upload_name(name: str | None, index: int) -> str:
    """Reduce a client-supplied file name to a safe basename."""
    base = Path((name or "").strip()).name
    base = _UNSAFE_FILENAME_CHARS.sub("_", base)
    base = base.lstrip(".")
    if not base:
        return f"upload-{index + 1}"
    return base


def unique_upload_path(directory: Path, name: str) -> Path:
    """Return a non-colliding path in directory, appending -1, -2, ... as needed."""
    path = directory / name
    if not path.exists():
        return path
    stem = Path(name).stem
    suffix = Path(name).suffix
    i = 1
    while True:
        candidate = directory / f"{stem}-{i}{suffix}"
        if not candidate.exists():
            return candidate
        i += 1


class UploadMaterializerPlugin(BasePlugin):
    """Writes inline file data from user messages into the session uploads/ dir."""

    def __init__(self) -> None:
        super().__init__(name="kagent_upload_materializer")

    async def on_user_message_callback(
        self,
        *,
        invocation_context: InvocationContext,
        user_message: types.Content,
    ) -> Optional[types.Content]:
        if not user_message or not user_message.parts:
            return None

        has_files = any(part.inline_data and part.inline_data.data for part in user_message.parts)
        if not has_files:
            return None

        session_id = invocation_context.session.id if invocation_context.session else None
        if not session_id:
            return None

        try:
            uploads_dir = get_session_path(session_id) / "uploads"
            uploads_dir.mkdir(parents=True, exist_ok=True)
        except Exception as e:  # noqa: BLE001 - keep the turn alive on workspace errors
            logger.error(f"Failed to resolve session uploads dir; keeping file parts inline: {e}")
            return None

        new_parts: list[types.Part] = []
        modified = False
        for i, part in enumerate(user_message.parts):
            blob = part.inline_data
            if not blob or not blob.data:
                new_parts.append(part)
                continue

            mime_type = blob.mime_type or "application/octet-stream"
            name = sanitize_upload_name(getattr(blob, "display_name", None), i)
            target = unique_upload_path(uploads_dir, name)
            try:
                target.write_bytes(blob.data)
            except OSError as e:
                logger.error(f"Failed to save uploaded file {name}; keeping part inline: {e}")
                new_parts.append(part)
                continue

            note = (
                f"[File uploaded: {target.name} ({mime_type}, {len(blob.data)} bytes). "
                f"It is saved in the session workspace at uploads/{target.name} — "
                f"use the file or bash tools to read it.]"
            )
            new_parts.append(types.Part(text=note))
            if mime_type.startswith("image/"):
                # Keep images inline as well so vision-capable models see them.
                new_parts.append(part)
            modified = True
            logger.info(f"Saved uploaded file to session workspace: {target}")

        if not modified:
            return None
        return types.Content(role=user_message.role, parts=new_parts)
