//go:build windows

package main

// 16x16 ICO file — a simple gradient square icon (blue to green)
// representing color depth. Generated as raw bytes to avoid
// needing an external icon file.
func generateTrayIcon() []byte {
	width, height := 16, 16
	bmpDataSize := width * height * 4
	bmpSize := 40 + bmpDataSize

	icoHeader := []byte{
		0, 0, // reserved
		1, 0, // type: icon
		1, 0, // count: 1 entry
	}

	icoEntry := []byte{
		byte(width),  // width
		byte(height), // height
		0,            // color count (0 = true color)
		0,            // reserved
		1, 0,         // color planes
		32, 0,        // bits per pixel
		byte(bmpSize), byte(bmpSize >> 8), byte(bmpSize >> 16), byte(bmpSize >> 24),
		22, 0, 0, 0, // offset to BMP data (6 + 16 = 22)
	}

	bmpHeader := []byte{
		40, 0, 0, 0, // header size
		byte(width), 0, 0, 0, // width
		byte(height * 2), 0, 0, 0, // height (doubled for ICO format)
		1, 0, // planes
		32, 0, // bits per pixel
		0, 0, 0, 0, // compression: none
		byte(bmpDataSize), byte(bmpDataSize >> 8), byte(bmpDataSize >> 16), byte(bmpDataSize >> 24),
		0, 0, 0, 0, // x pixels per meter
		0, 0, 0, 0, // y pixels per meter
		0, 0, 0, 0, // colors used
		0, 0, 0, 0, // important colors
	}

	pixels := make([]byte, bmpDataSize)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := (y*width + x) * 4
			r := byte(40 + x*10)
			g := byte(100 + y*8)
			b := byte(200 - x*6)
			// BGRA order for BMP
			pixels[offset+0] = b
			pixels[offset+1] = g
			pixels[offset+2] = r
			pixels[offset+3] = 255
		}
	}

	ico := make([]byte, 0, 22+bmpSize)
	ico = append(ico, icoHeader...)
	ico = append(ico, icoEntry...)
	ico = append(ico, bmpHeader...)
	ico = append(ico, pixels...)

	return ico
}
