package feeds

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

const ArticlePDFParserVersion = "article-pdf-v1"
const MaxPDFPages = 100
const MaxPDFTextBytes = 4 << 20

func ExtractArticlePDF(data []byte) (text string, err error) {
	return ExtractArticlePDFWithLimits(data, MaxPDFPages, MaxPDFTextBytes)
}

func ExtractArticlePDFWithLimits(data []byte, maxPages int, maxTextBytes int64) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = fmt.Errorf("parse article PDF: invalid input")
		}
	}()
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return "", fmt.Errorf("article PDF signature is missing")
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("parse article PDF: %w", err)
	}
	if reader.NumPage() < 1 || reader.NumPage() > maxPages {
		return "", fmt.Errorf("article PDF page count is outside 1..%d", maxPages)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract article PDF text: %w", err)
	}
	data, err = io.ReadAll(io.LimitReader(plain, maxTextBytes+1))
	if err != nil {
		return "", fmt.Errorf("read article PDF text: %w", err)
	}
	if int64(len(data)) > maxTextBytes {
		return "", fmt.Errorf("article PDF text exceeds %d bytes", maxTextBytes)
	}
	text = strings.Join(strings.Fields(string(data)), " ")
	if text == "" {
		return "", fmt.Errorf("article PDF contains no extractable text")
	}
	return text, nil
}
