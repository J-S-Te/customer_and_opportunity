# Portal PDF 水印字体

`UIPPortalSans-Regular.ttf` 是可再发行的静态 TrueType 字体，PostScript name 与文件 basename 均为 `UIPPortalSans-Regular`。生产水印使用 pdfcpu 安装后的 PostScript name 查找字体，部署时不得仅改文件名。

上游为 [Noto CJK Sans2.004](https://github.com/notofonts/noto-cjk/tree/523d033d6cb47f4a80c58a35753646f5c3608a78)，固定 commit `523d033d6cb47f4a80c58a35753646f5c3608a78`。原文件为官方区域版 `Sans/Variable/TTF/Subset/NotoSansSC-VF.ttf`；其默认字重为100，使用 fontTools 实例化为400，并修改 family/full/unique/PostScript names。完整保留官方区域字体的30,890个Unicode字符与原31,036个glyph；为兼容pdfcpu v0.8的glyph→单Unicode映射，复制共用glyph的轮廓/metrics，让每个Unicode码点有独立glyph ID，静态输出共31533个glyph。字形外观不变，不按测试水印文字子集裁剪。原始共用glyph会导致ASCII连字符被提取成U+2011，不能用于精确追踪码；本资产修复该兼容问题。

上游静态版本主要为OTF/OTC，本组件需要TrueType；因此交付静态TTF实例，不依赖宿主操作系统字体或运行时字体转换。实际支持的字符由cmap决定，不承诺所有Unicode；现有水印服务对缺字保持失败关闭。

版权：© 2014-2021 Adobe (http://www.adobe.com/), with Reserved Font Name 'Source'. 本修改版不使用保留名称，保留原字体嵌入的版权与许可信息。完整许可位于 [OFL.txt](OFL.txt)：SIL Open Font License 1.1。字体可随应用再发行、嵌入PDF；再发行时应保留本目录的许可与版权说明。字体没有安装或运行时商业授权要求。

## 交付校验

- 静态字体大小：10,699,952 bytes。
- SHA256：`648449aff608d6bd4700e724b4ef6a8f67d97dff8e9c728fafce4c7c4b08d4f9`。
- 固定源、原文件SHA、licenseSHA、工具版本、变换说明和覆盖计数： [SOURCE.json](SOURCE.json)。
- 全目录侧车与字体校验：`shasum -a 256 -c SHA256SUMS`。

## 重建

临时工作目录，不向项目添加Python依赖。以下命令从本目录执行；无密钥或生产凭据。

```sh
python3 -m venv /private/tmp/uip-portal-font-rebuild-venv
/private/tmp/uip-portal-font-rebuild-venv/bin/pip install --no-cache-dir fonttools==4.60.2
curl --fail --location 'https://raw.githubusercontent.com/notofonts/noto-cjk/523d033d6cb47f4a80c58a35753646f5c3608a78/Sans/Variable/TTF/Subset/NotoSansSC-VF.ttf' -o /private/tmp/uip-portal-font-source.ttf
/private/tmp/uip-portal-font-rebuild-venv/bin/python reproduce.py /private/tmp/uip-portal-font-source.ttf /private/tmp/UIPPortalSans-Regular.ttf
```

`reproduce.py`先核对源SHA、固定wght=400、替换标识名称、移除变量表，保留head timestamps，然后核对静态输出SHA。验证环境为Python3.9.6/fontTools4.60.2；正常运行Portal不需要Python/fontTools。

## 回归

从CRM模块目录运行：

```sh
UIP_TEST_PDF_RENDER_REQUIRED=1 GOWORK=off go test -race ./internal/reportsecurity -run TestBundledPortalFontRealChinesePDFWatermark -count=1 -v
```

真实`NewPDFWatermarker`与`Apply`加载本目录资产，核对watermark全文字符与printable ASCII均映射非zero glyph、PDF校验和分页。独立Poppler (`pdftotext`/`pdffonts`/`pdftoppm`) 必须验证中文字段、完整时区时间和tracking code可提取，且TrueType嵌入、实际PNG有可见水印。未安装独立工具时，默认子测试会说明skip；发布验证必须设置`UIP_TEST_PDF_RENDER_REQUIRED=1`使缺工具直接失败，不能将skip计作验收通过。
