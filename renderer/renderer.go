package renderer

import (
	"fmt"
	"image"
	"image/color"
	imagedraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CardData struct {
	PlayerName      string
	Map             string
	MapScore        string
	Kills           int
	Deaths          int
	KD              float64
	HeadshotsPct    float64
	Damage          int
	UtilityDamage   int
	OldElo          int
	NewElo          int
	EloChange       int
	Result          string
	MatchTime       time.Time
	CompetitionName string
	PhotoPath       string
	PhotoURL        string
	OutputPath      string
}

func RenderMatchCard(data CardData) error {
	const width = 1280
	const height = 1280

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	drawBackground(canvas)
	drawAccent(canvas)

	playerImg := loadPlayerImage(data.PhotoPath, data.PhotoURL)
	if playerImg != nil {
		drawPlayer(canvas, playerImg)
	} else {
		drawPlaceholder(canvas, data.PlayerName)
	}

	gold := color.RGBA{243, 201, 39, 255}
	white := color.RGBA{241, 243, 247, 255}
	muted := color.RGBA{170, 177, 194, 255}

	drawTextFit(canvas, strings.ToUpper(data.PlayerName), 64, 72, 620, 12, gold)
	if data.CompetitionName != "" {
		drawTextFit(canvas, strings.ToUpper(data.CompetitionName), 64, 160, 600, 5, muted)
	}
	drawMapResultHeader(canvas, data)

	boxX := 78
	boxY := 230
	boxW := 480
	boxH := 132
	gapY := 28
	stats := []struct {
		Value string
		Label string
		Scale int
	}{
		{fmt.Sprintf("%d-%d", data.Kills, data.Deaths), "K-D", 10},
		{fmt.Sprintf("%.2f", data.KD), "K/D", 10},
		{fmt.Sprintf("%.1f%%", data.HeadshotsPct), "HS", 9},
		{fmt.Sprintf("%d -> %d (%+d)", data.OldElo, data.NewElo, data.EloChange), "ELO", 5},
		{fmt.Sprintf("%d", data.Damage), "DAMAGE", 9},
		{fmt.Sprintf("%d", data.UtilityDamage), "UTILITY", 9},
	}
	for i, stat := range stats {
		y := boxY + i*(boxH+gapY)
		drawStatBox(canvas, boxX, y, boxW, boxH, stat.Value, stat.Label, stat.Scale)
	}

	drawText(canvas, data.MatchTime.Format("02.01.2006 15:04"), 78, 1220, 5, white)
	drawText(canvas, "FACEIT TELEGRAM BOT", 820, 1220, 4, gold)

	if err := os.MkdirAll(filepath.Dir(data.OutputPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(data.OutputPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, canvas, &jpeg.Options{Quality: 92})
}

func drawBackground(img *image.RGBA) {
	b := img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			fx := float64(x) / float64(b.Dx())
			fy := float64(y) / float64(b.Dy())
			r := uint8(8 + 10*fy)
			g := uint8(10 + 16*fx)
			bb := uint8(20 + 42*math.Max(fx, fy))
			img.Set(x, y, color.RGBA{r, g, bb, 255})
		}
	}
	imagedraw.Draw(img, image.Rect(0, 0, b.Dx(), b.Dy()), &image.Uniform{color.RGBA{0, 0, 0, 28}}, image.Point{}, imagedraw.Over)
}

func drawAccent(img *image.RGBA) {
	gold := &image.Uniform{color.RGBA{243, 201, 39, 255}}
	imagedraw.Draw(img, image.Rect(36, 228, 66, 1115), gold, image.Point{}, imagedraw.Src)
	imagedraw.Draw(img, image.Rect(660, 34, 1170, 186), &image.Uniform{color.RGBA{12, 16, 28, 190}}, image.Point{}, imagedraw.Over)
	imagedraw.Draw(img, image.Rect(660, 214, 1170, 1012), &image.Uniform{color.RGBA{12, 16, 28, 150}}, image.Point{}, imagedraw.Over)
	imagedraw.Draw(img, image.Rect(0, 1020, 1280, 1280), &image.Uniform{color.RGBA{0, 0, 0, 80}}, image.Point{}, imagedraw.Over)
}

func drawCenteredTextFit(dst *image.RGBA, text string, centerX, y, maxWidth, startScale int, col color.Color) {
	text = strings.ToUpper(text)
	scale := bestScale(text, startScale, maxWidth)
	drawText(dst, text, centerX-measureText(text, scale)/2, y, scale, col)
}

func bestScale(text string, startScale, maxWidth int) int {
	for scale := startScale; scale >= 2; scale-- {
		if measureText(text, scale) <= maxWidth {
			return scale
		}
	}
	return 2
}

type textSegment struct {
	Text  string
	Color color.Color
}

func bestScaleForSegments(values []string, startScale, maxWidth int) int {
	for scale := startScale; scale >= 2; scale-- {
		width := 0
		for _, value := range values {
			width += measureText(value, scale)
		}
		if width <= maxWidth {
			return scale
		}
	}
	return 2
}

func drawTextSegmentsCentered(dst *image.RGBA, segments []textSegment, centerX, y, scale int) {
	totalWidth := 0
	for _, segment := range segments {
		totalWidth += measureText(segment.Text, scale)
	}
	cx := centerX - totalWidth/2
	for _, segment := range segments {
		drawText(dst, segment.Text, cx, y, scale, segment.Color)
		cx += measureText(segment.Text, scale)
	}
}

func drawMapResultHeader(dst *image.RGBA, data CardData) {
	centerX := 915
	white := color.RGBA{241, 243, 247, 255}
	accent := resultColor(data.Result)
	mapPart := safeMap(data.Map)
	sepPart := " / "
	resultPart := resultLabel(data.Result)
	scale := bestScaleForSegments([]string{mapPart, sepPart, resultPart}, 7, 460)
	drawTextSegmentsCentered(dst, []textSegment{
		{Text: mapPart, Color: white},
		{Text: sepPart, Color: white},
		{Text: resultPart, Color: accent},
	}, centerX, 72, scale)
	if score := strings.TrimSpace(data.MapScore); score != "" {
		drawCenteredTextFit(dst, score, centerX, 132, 420, 8, accent)
	}
}

func resultColor(result string) color.RGBA {
	if strings.EqualFold(result, "win") {
		return color.RGBA{79, 214, 112, 255}
	}
	if strings.EqualFold(result, "loss") {
		return color.RGBA{232, 76, 94, 255}
	}
	return color.RGBA{241, 243, 247, 255}
}

func drawPlayer(dst *image.RGBA, src image.Image) {
	target := image.Rect(680, 235, 1150, 1007)
	cropped := cropToAspect(src, target.Dx(), target.Dy())
	scaled := scaleNearest(cropped, target.Dx(), target.Dy())
	imagedraw.Draw(dst, target, scaled, image.Point{}, imagedraw.Src)
	drawBorder(dst, target, color.RGBA{130, 137, 160, 210})
}

func drawPlaceholder(dst *image.RGBA, name string) {
	target := image.Rect(680, 235, 1150, 1007)
	imagedraw.Draw(dst, target, &image.Uniform{color.RGBA{33, 39, 58, 255}}, image.Point{}, imagedraw.Src)
	initials := strings.ToUpper(initials(name))
	w := measureText(initials, 18)
	drawText(dst, initials, target.Min.X+(target.Dx()-w)/2, target.Min.Y+target.Dy()/2-60, 18, color.RGBA{243, 201, 39, 255})
	drawBorder(dst, target, color.RGBA{130, 137, 160, 210})
}

func drawStatBox(dst *image.RGBA, x, y, w, h int, value, label string, scale int) {
	box := image.Rect(x, y, x+w, y+h)
	imagedraw.Draw(dst, box, &image.Uniform{color.RGBA{14, 18, 31, 215}}, image.Point{}, imagedraw.Over)
	border := color.RGBA{121, 129, 151, 180}
	for i := 0; i < 2; i++ {
		drawBorder(dst, image.Rect(x+i, y+i, x+w-i, y+h-i), border)
	}
	value = strings.ToUpper(value)
	vw := measureText(value, scale)
	drawText(dst, value, x+(w-vw)/2, y+18, scale, color.RGBA{243, 201, 39, 255})
	lw := measureText(label, 4)
	drawText(dst, label, x+(w-lw)/2, y+94, 4, color.RGBA{235, 238, 243, 255})
}

func drawBorder(dst *image.RGBA, r image.Rectangle, c color.Color) {
	for x := r.Min.X; x < r.Max.X; x++ {
		dst.Set(x, r.Min.Y, c)
		dst.Set(x, r.Max.Y-1, c)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		dst.Set(r.Min.X, y, c)
		dst.Set(r.Max.X-1, y, c)
	}
}

func drawTextFit(dst *image.RGBA, text string, x, y, maxWidth, startScale int, col color.Color) {
	for scale := startScale; scale >= 2; scale-- {
		if measureText(text, scale) <= maxWidth {
			drawText(dst, text, x, y, scale, col)
			return
		}
	}
	drawText(dst, text, x, y, 2, col)
}

func drawText(dst *image.RGBA, text string, x, y, scale int, col color.Color) {
	text = strings.ToUpper(text)
	cx := x
	for _, r := range text {
		glyph, ok := glyphs[r]
		if !ok {
			glyph = glyphs['?']
		}
		for row, line := range glyph {
			for colIdx, ch := range line {
				if ch != '1' {
					continue
				}
				drawBlock(dst, cx+colIdx*scale, y+row*scale, scale, col)
			}
		}
		cx += (glyphWidth(r) + 1) * scale
	}
}

func drawBlock(dst *image.RGBA, x, y, size int, col color.Color) {
	imagedraw.Draw(dst, image.Rect(x, y, x+size, y+size), &image.Uniform{col}, image.Point{}, imagedraw.Src)
}

func measureText(text string, scale int) int {
	text = strings.ToUpper(text)
	width := 0
	for _, r := range text {
		width += (glyphWidth(r) + 1) * scale
	}
	if width > 0 {
		width -= scale
	}
	return width
}

func glyphWidth(r rune) int {
	glyph, ok := glyphs[r]
	if !ok {
		glyph = glyphs['?']
	}
	maxW := 0
	for _, row := range glyph {
		if len(row) > maxW {
			maxW = len(row)
		}
	}
	return maxW
}

func cropToAspect(src image.Image, targetW, targetH int) image.Image {
	sb := src.Bounds()
	sw := sb.Dx()
	sh := sb.Dy()
	if sw == 0 || sh == 0 {
		return src
	}
	srcAspect := float64(sw) / float64(sh)
	targetAspect := float64(targetW) / float64(targetH)
	crop := sb
	if srcAspect > targetAspect {
		newW := int(float64(sh) * targetAspect)
		offset := (sw - newW) / 2
		crop = image.Rect(sb.Min.X+offset, sb.Min.Y, sb.Min.X+offset+newW, sb.Max.Y)
	} else {
		newH := int(float64(sw) / targetAspect)
		offset := (sh - newH) / 2
		crop = image.Rect(sb.Min.X, sb.Min.Y+offset, sb.Max.X, sb.Min.Y+offset+newH)
	}
	cropped := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	imagedraw.Draw(cropped, cropped.Bounds(), src, crop.Min, imagedraw.Src)
	return cropped
}

func scaleNearest(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + y*sb.Dy()/h
		for x := 0; x < w; x++ {
			sx := sb.Min.X + x*sb.Dx()/w
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func loadPlayerImage(localPath, remoteURL string) image.Image {
	if strings.TrimSpace(localPath) != "" {
		if file, err := os.Open(localPath); err == nil {
			defer file.Close()
			if img, _, err := image.Decode(file); err == nil {
				return img
			}
		}
	}
	if strings.TrimSpace(remoteURL) == "" {
		return nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(remoteURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil
	}
	return img
}

func safeMap(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(name)
}

func resultLabel(result string) string {
	if strings.EqualFold(result, "win") {
		return "WIN"
	}
	if strings.EqualFold(result, "loss") {
		return "LOSS"
	}
	return strings.ToUpper(result)
}

func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	if len(parts) == 1 {
		r := []rune(parts[0])
		if len(r) >= 2 {
			return string(r[:2])
		}
		return parts[0]
	}
	return string([]rune(parts[0])[:1]) + string([]rune(parts[1])[:1])
}

var glyphs = map[rune][]string{
	' ': {"000", "000", "000", "000", "000", "000", "000"},
	'?': {"11110", "00001", "00010", "00100", "00100", "00000", "00100"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'_': {"00000", "00000", "00000", "00000", "00000", "00000", "11111"},
	'/': {"00001", "00010", "00100", "01000", "10000", "00000", "00000"},
	'.': {"000", "000", "000", "000", "000", "000", "010"},
	':': {"000", "010", "000", "000", "010", "000", "000"},
	'%': {"11001", "11010", "00100", "01000", "10110", "00110", "00000"},
	'+': {"000", "010", "010", "111", "010", "010", "000"},
	'(': {"001", "010", "100", "100", "100", "010", "001"},
	')': {"100", "010", "001", "001", "001", "010", "100"},
	'>': {"100", "010", "001", "010", "100", "000", "000"},
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01110", "10000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00001", "01110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01110"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
	'J': {"00001", "00001", "00001", "00001", "10001", "10001", "01110"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "10101", "01010"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}
