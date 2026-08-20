from __future__ import annotations

from pathlib import Path


def split_sections(text: str) -> list[tuple[str | None, list[str]]]:
    sections: list[tuple[str | None, list[str]]] = []
    current_name: str | None = None
    current_lines: list[str] = []

    for line in text.splitlines():
        if line.startswith("[") and line.endswith("]"):
            sections.append((current_name, current_lines))
            current_name = line[1:-1]
            current_lines = [line]
        else:
            current_lines.append(line)

    sections.append((current_name, current_lines))
    return sections


def render_sections(sections: list[tuple[str | None, list[str]]]) -> str:
    rendered_chunks = []
    for _, lines in sections:
        chunk = "\n".join(lines).strip("\n")
        if chunk:
            rendered_chunks.append(chunk)
    return "\n\n".join(rendered_chunks) + "\n"


def update_config(config_path: Path, plugin_name: str, marketplace: str) -> None:
    plugin_key = f"{plugin_name}@{marketplace}"
    section_name = f'plugins."{plugin_key}"'

    existing_text = config_path.read_text(encoding="utf-8") if config_path.exists() else ""
    sections = split_sections(existing_text)
    filtered_sections = [(name, lines) for name, lines in sections if name != section_name]
    filtered_sections.append((section_name, [f"[{section_name}]", "enabled = true"]))

    config_path.parent.mkdir(parents=True, exist_ok=True)
    config_path.write_text(render_sections(filtered_sections), encoding="utf-8")
