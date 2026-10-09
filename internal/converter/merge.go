package converter

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// mergeCount 流式统计某工作表的合并单元格数量。
//
// 不用 excelize 的 GetMergeCells：它会走 workSheetReader，把整张工作表反序列化成
// xlsxWorksheet 并缓存在 File 里直到 Close（实测 150k 行 × 9 列要多花 855MB），
// 而我们只要一个数字。这里直接读 zip 里的 worksheet XML 逐 token 扫描，内存 O(1)；
// 任何一步失败都返回 error，由调用方降级为「不告警」。
func mergeCount(xlsxPath, sheet string) (int, error) {
	zr, err := zip.OpenReader(xlsxPath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()

	part, err := sheetXMLPart(zr, sheet)
	if err != nil {
		return 0, err
	}
	rc, err := openZipEntry(zr, part)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return scanMergeCells(rc)
}

// sheetXMLPart 通过 xl/workbook.xml（工作表名 → r:id）与 xl/_rels/workbook.xml.rels
// （r:id → 部件路径）定位工作表 XML。
func sheetXMLPart(zr *zip.ReadCloser, sheet string) (string, error) {
	rc, err := openZipEntry(zr, "xl/workbook.xml")
	if err != nil {
		return "", err
	}
	rid := ""
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			rc.Close()
			return "", err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sheet" {
			continue
		}
		if name, _ := attrValue(se, "name"); name == sheet {
			rid, _ = attrValue(se, "id") // r:id，忽略命名空间前缀差异
			break
		}
	}
	rc.Close()
	if rid == "" {
		return "", fmt.Errorf("sheet %q not found in workbook.xml", sheet)
	}

	rc, err = openZipEntry(zr, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", err
	}
	defer rc.Close()
	target := ""
	dec = xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		if id, _ := attrValue(se, "Id"); id != rid {
			continue
		}
		if mode, _ := attrValue(se, "TargetMode"); mode == "External" {
			return "", fmt.Errorf("sheet %q is external", sheet)
		}
		target, _ = attrValue(se, "Target")
		break
	}
	if target == "" {
		return "", fmt.Errorf("relationship %s has no target", rid)
	}
	// Target 可能是 "worksheets/sheet1.xml"（相对 xl/）或 "/xl/worksheets/sheet1.xml"
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/"), nil
	}
	return path.Clean(path.Join("xl", target)), nil
}

// afterMergeCells 是 OOXML 中排在 mergeCells 之后的元素：一旦遇到，说明本表
// 不会再有合并单元格，可以提前结束扫描（这些元素名不会出现在 mergeCells 之前）。
var afterMergeCells = map[string]bool{
	"phoneticPr": true, "conditionalFormatting": true, "dataValidations": true,
	"hyperlinks": true, "printOptions": true, "pageMargins": true, "pageSetup": true,
	"headerFooter": true, "rowBreaks": true, "colBreaks": true, "customProperties": true,
	"cellWatches": true, "ignoredErrors": true, "smartTags": true, "drawing": true,
	"legacyDrawing": true, "legacyDrawingHF": true, "picture": true, "oleObjects": true,
	"controls": true, "webPublishItems": true, "tableParts": true, "extLst": true,
}

func scanMergeCells(r io.Reader) (int, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case se.Name.Local == "mergeCells":
			if n, ok := attrInt(se, "count"); ok {
				return n, nil // 有 count 属性，连子元素都不用读
			}
			return countMergeCellChildren(dec)
		case afterMergeCells[se.Name.Local]:
			return 0, nil
		}
	}
}

func countMergeCellChildren(dec *xml.Decoder) (int, error) {
	n := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "mergeCell" {
				n++
			}
		case xml.EndElement:
			if t.Name.Local == "mergeCells" {
				return n, nil
			}
		}
	}
}

// openZipEntry 按名字（忽略大小写）打开 zip 条目。
func openZipEntry(zr *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, name) {
			return f.Open()
		}
	}
	return nil, fmt.Errorf("zip entry %s not found", name)
}

func attrValue(se xml.StartElement, local string) (string, bool) {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value, true
		}
	}
	return "", false
}

func attrInt(se xml.StartElement, local string) (int, bool) {
	v, ok := attrValue(se, local)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}
