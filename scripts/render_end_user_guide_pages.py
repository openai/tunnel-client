#!/usr/bin/env python3

from __future__ import annotations

import importlib
import sys
import warnings
from pathlib import Path
from typing import Iterable

from PIL import Image, ImageDraw, ImageFont

warnings.filterwarnings(
    "ignore",
    message="Palette images with Transparency expressed in bytes should be converted to RGBA images",
    category=UserWarning,
    module="PIL.Image",
)

CLIENT_ROOT = Path(__file__).resolve().parents[1]
DOCS_ROOT = CLIENT_ROOT / "docs"
OUTPUT_ROOT = DOCS_ROOT / "output"
PAGE_OUTPUT_ROOT = OUTPUT_ROOT / "end-user-guide-pages"

sys.path.insert(0, str(DOCS_ROOT))
page_blocks = importlib.import_module("pdf.end_user_guide_page_blocks")
PAGE_BACKGROUNDS = page_blocks.PAGE_BACKGROUNDS
PAGE_BLOCKS = page_blocks.PAGE_BLOCKS


PAGE_W = 1200
PAGE_H = 1553
MARGIN_X = 88
MARGIN_Y = 86
CONTENT_W = PAGE_W - 2 * MARGIN_X

NAVY = "#16314a"
TEAL = "#167f7a"
AMBER = "#a56c10"
INK = "#132335"
MUTED = "#5a6a7a"
CODE_BG = "#152632"
CODE_FG = "#eef6fb"
PANEL = (255, 252, 245, 228)
PANEL_STRONG = (255, 253, 248, 240)
ACCENT_PANEL = (255, 247, 224, 238)
SHADOW = (25, 40, 55, 40)
LINK = "#0f7e89"

GEORGIA = "/System/Library/Fonts/Supplemental/Georgia.ttf"
GEORGIA_BOLD = "/System/Library/Fonts/Supplemental/Georgia Bold.ttf"
GEORGIA_ITALIC = "/System/Library/Fonts/Supplemental/Georgia Italic.ttf"
SF = "/System/Library/Fonts/SFNS.ttf"
SF_BOLD = "/System/Library/Fonts/SFNSRounded.ttf"
SF_MONO = "/System/Library/Fonts/SFNSMono.ttf"

TITLE_FONT = ImageFont.truetype(GEORGIA_BOLD, 72)
PAGE_TITLE_FONT = ImageFont.truetype(SF_BOLD, 50)
KICKER_FONT = ImageFont.truetype(SF, 20)
DECK_FONT = ImageFont.truetype(SF, 30)
BODY_FONT = ImageFont.truetype(GEORGIA, 28)
BODY_BOLD_FONT = ImageFont.truetype(GEORGIA_BOLD, 28)
SMALL_FONT = ImageFont.truetype(GEORGIA, 23)
SMALL_BOLD_FONT = ImageFont.truetype(GEORGIA_BOLD, 23)
CAPTION_FONT = ImageFont.truetype(GEORGIA, 21)
LABEL_FONT = ImageFont.truetype(SF, 18)
PILL_FONT = ImageFont.truetype(SF_BOLD, 18)
CODE_FONT = ImageFont.truetype(SF_MONO, 23)


def ensure_dirs() -> None:
    OUTPUT_ROOT.mkdir(parents=True, exist_ok=True)
    PAGE_OUTPUT_ROOT.mkdir(parents=True, exist_ok=True)


def clean_text(text: str) -> str:
    replacements = {
        "&gt;": ">",
        "&lt;": "<",
        "&amp;": "&",
        "`": "",
        "**": "",
    }
    for src, dst in replacements.items():
        text = text.replace(src, dst)
    return " ".join(text.split())


def fit_background(path: Path) -> Image.Image:
    bg = Image.open(path).convert("RGB")
    target_ratio = PAGE_W / PAGE_H
    src_ratio = bg.width / bg.height
    if src_ratio > target_ratio:
        new_w = int(bg.height * target_ratio)
        left = (bg.width - new_w) // 2
        bg = bg.crop((left, 0, left + new_w, bg.height))
    else:
        new_h = int(bg.width / target_ratio)
        top = (bg.height - new_h) // 2
        bg = bg.crop((0, top, bg.width, top + new_h))
    return bg.resize((PAGE_W, PAGE_H), Image.Resampling.LANCZOS).convert("RGBA")


def draw_shadowed_panel(
    base: Image.Image, box: tuple[int, int, int, int], radius: int, fill: tuple[int, int, int, int]
) -> None:
    overlay = Image.new("RGBA", base.size, (0, 0, 0, 0))
    draw = ImageDraw.Draw(overlay)
    shadow_box = (box[0] + 6, box[1] + 8, box[2] + 6, box[3] + 8)
    draw.rounded_rectangle(shadow_box, radius=radius, fill=SHADOW)
    draw.rounded_rectangle(box, radius=radius, fill=fill, outline=(24, 43, 65, 28), width=2)
    base.alpha_composite(overlay)


def text_width(draw: ImageDraw.ImageDraw, text: str, font: ImageFont.FreeTypeFont) -> int:
    return draw.textbbox((0, 0), text, font=font)[2]


def line_height(font: ImageFont.FreeTypeFont, extra: int = 0) -> int:
    bbox = font.getbbox("Ag")
    return bbox[3] - bbox[1] + extra


def wrap_text(
    draw: ImageDraw.ImageDraw, text: str, font: ImageFont.FreeTypeFont, max_width: int
) -> list[str]:
    words = clean_text(text).split()
    lines: list[str] = []
    current: list[str] = []
    for word in words:
        candidate = " ".join(current + [word])
        if current and text_width(draw, candidate, font) > max_width:
            lines.append(" ".join(current))
            current = [word]
        else:
            current.append(word)
    if current:
        lines.append(" ".join(current))
    return lines or [""]


def draw_paragraph(
    draw: ImageDraw.ImageDraw,
    x: int,
    y: int,
    text: str,
    font: ImageFont.FreeTypeFont,
    max_width: int,
    fill: str = INK,
) -> int:
    lines = wrap_text(draw, text, font, max_width)
    lh = line_height(font, 8)
    for line in lines:
        draw.text((x, y), line, font=font, fill=fill)
        y += lh
    return y


def draw_bullets(
    draw: ImageDraw.ImageDraw,
    x: int,
    y: int,
    items: Iterable[str],
    max_width: int,
    font: ImageFont.FreeTypeFont = BODY_FONT,
) -> int:
    bullet_gap = 14
    bullet_x = x + 6
    text_x = x + 34
    text_width_limit = max_width - 34
    lh = line_height(font, 8)
    for item in items:
        lines = wrap_text(draw, item, font, text_width_limit)
        draw.ellipse((bullet_x, y + 12, bullet_x + 8, y + 20), fill=TEAL)
        for idx, line in enumerate(lines):
            draw.text((text_x, y + idx * lh), line, font=font, fill=INK)
        y += len(lines) * lh + bullet_gap
    return y


def draw_code_block(
    base: Image.Image, draw: ImageDraw.ImageDraw, x: int, y: int, text: str, max_width: int
) -> int:
    raw_lines = [line.rstrip() for line in text.splitlines()]
    wrapped: list[str] = []
    for raw in raw_lines:
        words = raw.split(" ")
        current = ""
        for word in words:
            candidate = f"{current} {word}".strip()
            if current and text_width(draw, candidate, CODE_FONT) > max_width - 56:
                wrapped.append(current)
                current = word
            else:
                current = candidate
        wrapped.append(current or "")
    lh = line_height(CODE_FONT, 10)
    block_h = 34 + len(wrapped) * lh
    box = (x, y, x + max_width, y + block_h)
    draw_shadowed_panel(base, box, 26, (21, 38, 50, 240))
    cursor_y = y + 18
    for line in wrapped:
        draw.text((x + 26, cursor_y), line, font=CODE_FONT, fill=CODE_FG)
        cursor_y += lh
    return box[3]


def paste_cover(
    base: Image.Image, image_path: Path, box: tuple[int, int, int, int], fit: str = "contain"
) -> None:
    img = Image.open(image_path).convert("RGB")
    target_w = box[2] - box[0]
    target_h = box[3] - box[1]
    if fit == "cover":
        scale = max(target_w / img.width, target_h / img.height)
    else:
        scale = min(target_w / img.width, target_h / img.height)
    new_size = (max(1, int(img.width * scale)), max(1, int(img.height * scale)))
    resized = img.resize(new_size, Image.Resampling.LANCZOS)
    if fit == "cover":
        left = max(0, (resized.width - target_w) // 2)
        top = max(0, (resized.height - target_h) // 2)
        resized = resized.crop((left, top, left + target_w, top + target_h))
    canvas = Image.new("RGBA", base.size, (0, 0, 0, 0))
    x = box[0] + (target_w - resized.width) // 2
    y = box[1] + (target_h - resized.height) // 2
    canvas.paste(resized, (x, y))
    base.alpha_composite(canvas)


def draw_image_card(
    base: Image.Image,
    draw: ImageDraw.ImageDraw,
    x: int,
    y: int,
    w: int,
    h: int,
    rel_path: str,
    caption: str,
    fit: str = "contain",
) -> int:
    box = (x, y, x + w, y + h)
    draw_shadowed_panel(base, box, 28, PANEL_STRONG)
    image_box = (x + 18, y + 18, x + w - 18, y + h - 84)
    paste_cover(base, DOCS_ROOT / rel_path, image_box, fit=fit)
    caption_y = y + h - 60
    draw_paragraph(draw, x + 20, caption_y, caption, CAPTION_FONT, w - 40, MUTED)
    return y + h


def render_cover(spec: dict, bg: Image.Image) -> Image.Image:
    draw = ImageDraw.Draw(bg)
    hero_box = (MARGIN_X, 86, PAGE_W - MARGIN_X, 642)
    draw_shadowed_panel(bg, hero_box, 42, (18, 58, 71, 196))
    text_x = hero_box[0] + 34
    title_y = hero_box[1] + 30
    draw.text((text_x, title_y), spec["kicker"].upper(), font=KICKER_FONT, fill=(245, 232, 205))
    title_y += 34
    title_lines = wrap_text(draw, spec["title"], TITLE_FONT, 560)
    for line in title_lines:
        draw.text((text_x, title_y), line, font=TITLE_FONT, fill="white")
        title_y += line_height(TITLE_FONT, 6)
    title_y += 10
    deck_lines = wrap_text(draw, spec["deck"], DECK_FONT, 560)
    for line in deck_lines:
        draw.text((text_x, title_y), line, font=DECK_FONT, fill=(240, 248, 250))
        title_y += line_height(DECK_FONT, 10)

    rail_x = hero_box[0] + 640
    rail_y = hero_box[1] + 46
    draw.text((rail_x, rail_y), "INSIDE THIS GUIDE", font=KICKER_FONT, fill=(245, 232, 205))
    rail_y += 34
    for pill in spec["pills"]:
        pill_w = 300
        pill_h = 52
        draw.rounded_rectangle(
            (rail_x, rail_y, rail_x + pill_w, rail_y + pill_h),
            radius=24,
            fill=(252, 246, 233, 232),
            outline=(255, 255, 255, 80),
            width=2,
        )
        draw.text((rail_x + 18, rail_y + 14), pill, font=LABEL_FONT, fill=NAVY)
        rail_y += pill_h + 14

    y = hero_box[3] + 28
    y = draw_paragraph(draw, MARGIN_X, y, spec["paragraphs"][0], BODY_FONT, CONTENT_W)

    pair = spec["callout_pair"]
    y += 28
    card_gap = 24
    card_w = (CONTENT_W - card_gap) // 2
    card_h = 392
    left_box = (MARGIN_X, y, MARGIN_X + card_w, y + card_h)
    right_box = (left_box[2] + card_gap, y, PAGE_W - MARGIN_X, y + card_h)
    draw_shadowed_panel(bg, left_box, 34, PANEL_STRONG)
    draw_shadowed_panel(bg, right_box, 34, ACCENT_PANEL)
    draw.text(
        (left_box[0] + 22, left_box[1] + 22),
        pair["left"]["title"].upper(),
        font=KICKER_FONT,
        fill=NAVY,
    )
    draw.text(
        (right_box[0] + 22, right_box[1] + 22),
        pair["right"]["title"].upper(),
        font=KICKER_FONT,
        fill=AMBER,
    )
    draw_bullets(
        draw, left_box[0] + 14, left_box[1] + 62, pair["left"]["bullets"], card_w - 30, SMALL_FONT
    )
    draw_paragraph(
        draw, right_box[0] + 22, right_box[1] + 78, pair["right"]["text"], BODY_FONT, card_w - 44
    )
    return bg


def render_standard_page(spec: dict, bg: Image.Image) -> Image.Image:
    draw = ImageDraw.Draw(bg)
    plate_box = (46, 46, PAGE_W - 46, PAGE_H - 46)
    draw_shadowed_panel(bg, plate_box, 40, (255, 250, 242, 168))
    x = MARGIN_X
    y = MARGIN_Y
    draw.text((x, y), spec["title"], font=PAGE_TITLE_FONT, fill=NAVY)
    y += 68

    for paragraph in spec.get("paragraphs", []):
        y = draw_paragraph(draw, x, y, paragraph, BODY_FONT, CONTENT_W)
        y += 18

    if spec.get("link_list"):
        for label, target in spec["link_list"]:
            line = f"{label}: {target}"
            y = draw_paragraph(draw, x, y, line, SMALL_FONT, CONTENT_W, LINK)
            y += 10
        y += 10

    if spec.get("key_cards"):
        for card in spec["key_cards"]:
            box = (x, y, x + CONTENT_W, y + 220)
            draw_shadowed_panel(bg, box, 26, PANEL_STRONG)
            cy = box[1] + 18
            draw.text((box[0] + 22, cy), card["title"], font=LABEL_FONT, fill=NAVY)
            cy += 34
            draw.text((box[0] + 22, cy), "WHERE YOU GET IT", font=KICKER_FONT, fill=MUTED)
            cy += 22
            cy = draw_paragraph(draw, box[0] + 22, cy, card["where"], SMALL_FONT, CONTENT_W - 44)
            cy += 12
            draw.text((box[0] + 22, cy), "WHAT IT IS FOR", font=KICKER_FONT, fill=MUTED)
            cy += 22
            cy = draw_paragraph(draw, box[0] + 22, cy, card["what"], SMALL_FONT, CONTENT_W - 44)
            pill_label = f"When you need it: {card['when']}"
            pill_w = min(CONTENT_W - 44, text_width(draw, pill_label, SMALL_BOLD_FONT) + 32)
            pill_h = 38
            pill_y = box[3] - pill_h - 18
            draw.rounded_rectangle(
                (box[0] + 22, pill_y, box[0] + 22 + pill_w, pill_y + pill_h),
                radius=18,
                fill=(249, 236, 202, 230),
                outline=(196, 155, 84, 70),
                width=2,
            )
            draw.text((box[0] + 38, pill_y + 8), pill_label, font=SMALL_BOLD_FONT, fill=NAVY)
            y = box[3] + 18

    if spec.get("code_blocks"):
        for block in spec["code_blocks"]:
            y = draw_code_block(bg, draw, x, y, block, CONTENT_W) + 22

    if spec.get("image_row"):
        count = len(spec["image_row"])
        gap = 22
        row_h = spec.get("image_row_height", 320)
        if count == 1:
            item = spec["image_row"][0]
            y = (
                draw_image_card(
                    bg,
                    draw,
                    x,
                    y,
                    CONTENT_W,
                    row_h,
                    item["path"],
                    item["caption"],
                    fit=item.get("fit", "contain"),
                )
                + 22
            )
        else:
            card_w = (CONTENT_W - gap) // count
            max_bottom = y
            for idx, item in enumerate(spec["image_row"]):
                bottom = draw_image_card(
                    bg,
                    draw,
                    x + idx * (card_w + gap),
                    y,
                    card_w,
                    row_h,
                    item["path"],
                    item["caption"],
                    fit=item.get("fit", "contain"),
                )
                max_bottom = max(max_bottom, bottom)
            y = max_bottom + 22

    if spec.get("image_stack"):
        stack_h = spec.get("image_stack_height", 330)
        stack_w = spec.get("image_stack_width", CONTENT_W)
        stack_x = x + (CONTENT_W - stack_w) // 2
        for item in spec["image_stack"]:
            y = (
                draw_image_card(
                    bg,
                    draw,
                    stack_x,
                    y,
                    stack_w,
                    stack_h,
                    item["path"],
                    item["caption"],
                    fit=item.get("fit", "contain"),
                )
                + 22
            )

    if spec.get("bullets"):
        y = draw_bullets(draw, x, y, spec["bullets"], CONTENT_W)
        y += 6

    if spec.get("faq_list"):
        for faq in spec["faq_list"]:
            q_box = (x, y, x + CONTENT_W, y + 138)
            draw_shadowed_panel(bg, q_box, 24, PANEL_STRONG)
            draw.text((q_box[0] + 22, q_box[1] + 18), faq["q"], font=SMALL_BOLD_FONT, fill=NAVY)
            draw_paragraph(draw, q_box[0] + 22, q_box[1] + 58, faq["a"], SMALL_FONT, CONTENT_W - 44)
            y = q_box[3] + 14

    if y > PAGE_H - 70:
        print(f"warning: page '{spec['id']}' content reached y={y}", file=sys.stderr)
    return bg


def render_pages() -> list[Path]:
    ensure_dirs()
    outputs: list[Path] = []
    for index, spec in enumerate(PAGE_BLOCKS, start=1):
        bg = fit_background(DOCS_ROOT / PAGE_BACKGROUNDS[spec["background"]])
        if spec["id"] == "cover":
            page = render_cover(spec, bg)
        else:
            page = render_standard_page(spec, bg)
        out_path = PAGE_OUTPUT_ROOT / f"{index:02d}-{spec['id']}.png"
        page.convert("RGB").save(out_path, "PNG")
        outputs.append(out_path)
    return outputs


def main() -> int:
    page_paths = render_pages()
    print(f"pages: {len(page_paths)}")
    for path in page_paths:
        print(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
