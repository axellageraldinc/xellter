package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"mime/multipart"
)

// multipartFile returns the bytes of the "file" part of a multipart body (the audio of a transcription).
func multipartFile(raw []byte, boundary string) []byte {
	if boundary == "" {
		return nil
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil
		}
		if p.FormName() == "file" {
			b, _ := io.ReadAll(p)
			return b
		}
	}
}

// audioSeconds is the length of an audio file in seconds, read from its container header (WAV, FLAC, Ogg
// Opus/Vorbis, MP4/M4A, MP3, WebM with a Duration). It returns 0 when the length cannot be told, and the caller
// bills a nominal minute then. Nothing is decoded.
func audioSeconds(d []byte) float64 {
	var s float64
	switch {
	case len(d) > 12 && string(d[:4]) == "RIFF" && string(d[8:12]) == "WAVE":
		s = wavSeconds(d)
	case len(d) > 26 && string(d[:4]) == "fLaC":
		s = flacSeconds(d)
	case len(d) > 4 && string(d[:4]) == "OggS":
		s = oggSeconds(d)
	case len(d) > 12 && string(d[4:8]) == "ftyp":
		s = mp4Seconds(d)
	case len(d) > 4 && d[0] == 0x1A && d[1] == 0x45 && d[2] == 0xDF && d[3] == 0xA3:
		s = webmSeconds(d)
	default:
		s = mp3Seconds(d)
	}
	if math.IsNaN(s) || math.IsInf(s, 0) || s < 0 || s > 24*3600 {
		return 0
	}
	return s
}

func wavSeconds(d []byte) float64 {
	var byteRate, size uint32
	for p := 12; p+8 <= len(d); {
		id, n := string(d[p:p+4]), binary.LittleEndian.Uint32(d[p+4:p+8])
		body := p + 8
		switch id {
		case "fmt ":
			if body+12 <= len(d) {
				byteRate = binary.LittleEndian.Uint32(d[body+8 : body+12])
			}
		case "data":
			size = n
			if rest := uint32(len(d) - body); size == 0 || size == 0xFFFFFFFF || size > rest {
				size = rest // streamed or truncated file: the data runs to the end
			}
			if byteRate == 0 {
				return 0
			}
			return float64(size) / float64(byteRate)
		}
		p = body + int(n) + int(n&1)
		if n > uint32(len(d)) {
			break
		}
	}
	return 0
}

func flacSeconds(d []byte) float64 {
	rate := uint32(d[18])<<12 | uint32(d[19])<<4 | uint32(d[20])>>4
	total := uint64(d[20]&0x0F)<<32 | uint64(d[21])<<24 | uint64(d[22])<<16 | uint64(d[23])<<8 | uint64(d[24])
	if rate == 0 {
		return 0
	}
	return float64(total) / float64(rate)
}

func oggSeconds(d []byte) float64 {
	rate, skip := 0.0, 0.0
	if i := bytes.Index(d[:min(len(d), 512)], []byte("OpusHead")); i >= 0 && i+12 <= len(d) {
		rate, skip = 48000, float64(binary.LittleEndian.Uint16(d[i+10:i+12]))
	} else if i := bytes.Index(d[:min(len(d), 512)], []byte("\x01vorbis")); i >= 0 && i+16 <= len(d) {
		rate = float64(binary.LittleEndian.Uint32(d[i+12 : i+16]))
	}
	if rate == 0 {
		return 0
	}
	last := bytes.LastIndex(d, []byte("OggS"))
	if last < 0 || last+14 > len(d) {
		return 0
	}
	g := binary.LittleEndian.Uint64(d[last+6 : last+14])
	if g == math.MaxUint64 {
		return 0
	}
	return (float64(g) - skip) / rate
}

func mp4Seconds(d []byte) float64 {
	i := bytes.Index(d, []byte("mvhd"))
	if i < 0 || i+4+4 > len(d) {
		return 0
	}
	b := d[i+4:]
	switch b[0] {
	case 0:
		if len(b) < 20 {
			return 0
		}
		ts, dur := binary.BigEndian.Uint32(b[12:16]), binary.BigEndian.Uint32(b[16:20])
		if ts == 0 {
			return 0
		}
		return float64(dur) / float64(ts)
	case 1:
		if len(b) < 32 {
			return 0
		}
		ts, dur := binary.BigEndian.Uint32(b[20:24]), binary.BigEndian.Uint64(b[24:32])
		if ts == 0 {
			return 0
		}
		return float64(dur) / float64(ts)
	}
	return 0
}

// webmSeconds reads the Duration element of the segment info. A recording made by a browser often has none,
// and then the length is unknown.
func webmSeconds(d []byte) float64 {
	head := d[:min(len(d), 4096)]
	scale := 1e6 // ns per tick, the Matroska default
	if i := bytes.Index(head, []byte{0x2A, 0xD7, 0xB1}); i >= 0 && i+4 < len(head) {
		if n := int(head[i+3] & 0x0F); head[i+3]&0x80 != 0 && n >= 1 && n <= 8 && i+4+n <= len(head) {
			var v uint64
			for _, c := range head[i+4 : i+4+n] {
				v = v<<8 | uint64(c)
			}
			scale = float64(v)
		}
	}
	i := bytes.Index(head, []byte{0x44, 0x89})
	if i < 0 || i+3 >= len(head) {
		return 0
	}
	var ticks float64
	switch head[i+2] {
	case 0x84:
		if i+7 > len(head) {
			return 0
		}
		ticks = float64(math.Float32frombits(binary.BigEndian.Uint32(head[i+3 : i+7])))
	case 0x88:
		if i+11 > len(head) {
			return 0
		}
		ticks = math.Float64frombits(binary.BigEndian.Uint64(head[i+3 : i+11]))
	default:
		return 0
	}
	return ticks * scale / 1e9
}

var (
	mp3Rates = [4][3]int{{11025, 12000, 8000}, {0, 0, 0}, {22050, 24000, 16000}, {44100, 48000, 32000}}
	mp3V1L3  = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3V2L3  = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
)

// mp3Seconds reads the first Layer III frame: the frame count of a Xing/Info tag when there is one, else the
// bitrate of the first frame over the file size (right for constant bitrate).
func mp3Seconds(d []byte) float64 {
	off := 0
	if len(d) > 10 && string(d[:3]) == "ID3" {
		off = 10 + (int(d[6]&0x7F)<<21 | int(d[7]&0x7F)<<14 | int(d[8]&0x7F)<<7 | int(d[9]&0x7F))
	}
	for i := off; i+4 <= len(d) && i < off+8192; i++ {
		if d[i] != 0xFF || d[i+1]&0xE0 != 0xE0 {
			continue
		}
		ver, layer := int(d[i+1]>>3&3), int(d[i+1]>>1&3)
		if ver == 1 || layer != 1 { // reserved version, or not Layer III
			continue
		}
		bri, sri := int(d[i+2]>>4), int(d[i+2]>>2&3)
		if bri == 0 || bri == 15 || sri == 3 {
			continue
		}
		rate := mp3Rates[ver][sri]
		kbps := mp3V1L3[bri]
		spf := 1152.0
		if ver != 3 {
			kbps, spf = mp3V2L3[bri], 576
		}
		mono := d[i+3]>>6 == 3
		side := 17 // bytes between the header and the Xing tag
		switch {
		case ver == 3 && !mono:
			side = 32
		case ver != 3 && mono:
			side = 9
		}
		if x := i + 4 + side; x+12 <= len(d) && (string(d[x:x+4]) == "Xing" || string(d[x:x+4]) == "Info") && d[x+7]&1 != 0 {
			return float64(binary.BigEndian.Uint32(d[x+8:x+12])) * spf / float64(rate)
		}
		return float64(len(d)-i) * 8 / float64(kbps*1000)
	}
	return 0
}
