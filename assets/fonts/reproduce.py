#!/usr/bin/env python3
"""从固定版本的上游 TTF 构建重命名后的门户静态常规字体。

在独立虚拟环境中使用 fonttools==4.60.2 运行。输入须为 SOURCE.json
标识的完整上游 NotoSansSC-VF.ttf，不进行字形子集裁剪。
"""
import argparse
import copy
import fontTools
import hashlib
import json
from pathlib import Path
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont


def build_font(source):
    font = TTFont(source, recalcTimestamp=False)
    font = instantiateVariableFont(font, {'wght': 400}, inplace=True, optimize=True)
    font.recalcTimestamp = False
    names = {1: 'UIPPortalSans', 2: 'Regular', 3: 'UIPPortalSans-Regular',
             4: 'UIPPortalSans Regular', 6: 'UIPPortalSans-Regular',
             16: 'UIPPortalSans', 17: 'Regular', 18: 'UIPPortalSans Regular',
             25: 'UIPPortalSans'}
    for record in font['name'].names:
        if record.nameID in names:
            record.string = names[record.nameID].encode(record.getEncoding())
    if any(table in font for table in ('fvar', 'gvar', 'avar', 'HVAR', 'MVAR')):
        raise SystemExit('output still contains variable font tables')
    # pdfcpu v0.8 的 ToUnicode 映射只能将一个字形对应到一个 Unicode 字符。
    # 共享的 cmap 字形会把 ASCII 连字符转换为 U+2011，可能改变提取出的追踪标识。
    # 保留全部字符与轮廓，并复制共享的轮廓及度量，
    # 使每个 Unicode 字符都有独立字形 ID，且不改变外观。
    order = font.getGlyphOrder()
    assigned = set()
    replacements = {}
    for codepoint, glyph in sorted(font.getBestCmap().items()):
        if glyph not in assigned:
            assigned.add(glyph)
            continue
        unique = 'UIPuni%06X' % codepoint
        if unique in font['glyf'].glyphs:
            raise SystemExit('unexpected duplicate glyph name')
        font['glyf'].glyphs[unique] = copy.deepcopy(font['glyf'][glyph])
        font['hmtx'].metrics[unique] = font['hmtx'].metrics[glyph]
        if 'vmtx' in font:
            font['vmtx'].metrics[unique] = font['vmtx'].metrics[glyph]
        order.append(unique)
        replacements[codepoint] = unique
    font.setGlyphOrder(order)
    for table in font['cmap'].tables:
        if table.isUnicode() and hasattr(table, 'cmap'):
            for codepoint, unique in replacements.items():
                if codepoint in table.cmap:
                    table.cmap[codepoint] = unique
    font['maxp'].numGlyphs = len(order)
    return font


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('source', type=Path)
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    if fontTools.__version__ != '4.60.2':
        raise SystemExit('fontTools 4.60.2 is required for byte-reproducible output')
    metadata = json.loads(Path(__file__).with_name('SOURCE.json').read_text())
    source_hash = hashlib.sha256(args.source.read_bytes()).hexdigest()
    if source_hash != metadata['source_sha256']:
        raise SystemExit('source SHA256 mismatch')
    font = build_font(args.source)
    font.save(args.output, reorderTables=True)
    result_hash = hashlib.sha256(args.output.read_bytes()).hexdigest()
    if result_hash != metadata['static_sha256']:
        raise SystemExit('static SHA256 mismatch: ' + result_hash)
    print(result_hash + '  ' + args.output.name)


if __name__ == '__main__':
    main()
