package proxy

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.05 {
		t.Errorf("%s: %.3f s, want %.3f s", name, got, want)
	}
}

func TestAudioSecondsWAV(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&b, binary.LittleEndian, uint32(16000))
	binary.Write(&b, binary.LittleEndian, uint32(32000)) // byte rate
	binary.Write(&b, binary.LittleEndian, []uint16{2, 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(32000*7))
	b.Write(make([]byte, 32000*7))
	near(t, "wav", audioSeconds(b.Bytes()), 7)
}

func TestAudioSecondsFLAC(t *testing.T) {
	d := make([]byte, 40)
	copy(d, "fLaC")
	rate, total := uint64(16000), uint64(16000*12)
	d[18], d[19], d[20] = byte(rate>>12), byte(rate>>4), byte(rate<<4)|byte(total>>32&0x0F)
	binary.BigEndian.PutUint32(d[21:25], uint32(total))
	near(t, "flac", audioSeconds(d), 12)
}

func TestAudioSecondsOpus(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("OggS\x00\x02" + string(make([]byte, 20)))
	b.WriteString("OpusHead\x01\x01\x38\x01")
	b.Write(make([]byte, 40))
	b.WriteString("OggS\x00\x04")
	binary.Write(&b, binary.LittleEndian, uint64(48000*5+312))
	b.Write(make([]byte, 20))
	near(t, "opus", audioSeconds(b.Bytes()), 5)
}

func TestAudioSecondsMP4(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00M4A ")
	b.WriteString("\x00\x00\x00\x6cmvhd\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	binary.Write(&b, binary.BigEndian, uint32(1000))
	binary.Write(&b, binary.BigEndian, uint32(9500))
	near(t, "mp4", audioSeconds(b.Bytes()), 9.5)
}

func TestAudioSecondsMP3CBR(t *testing.T) {
	// MPEG1 Layer III, 128 kbps, 44.1 kHz, stereo: 16000 bytes per second
	d := append([]byte{0xFF, 0xFB, 0x90, 0x00}, make([]byte, 16000*4-4)...)
	near(t, "mp3", audioSeconds(d), 4)
}

func TestAudioSecondsUnknown(t *testing.T) {
	if got := audioSeconds([]byte("not audio at all")); got != 0 {
		t.Errorf("unknown audio: %v, want 0", got)
	}
	if got := audioSeconds(nil); got != 0 {
		t.Errorf("nil: %v, want 0", got)
	}
}
