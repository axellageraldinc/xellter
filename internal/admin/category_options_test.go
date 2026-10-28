package admin

import (
	"testing"

	"github.com/axellageraldinc/xellter/internal/config"
)

// The Add preset list offers Voice Conversation as its own entry with a fixed name, next to the ordinary chat one (ADR 0038).
func TestCategoryOptionsOfferVoiceConversationNextToText(t *testing.T) {
	var chat []categoryOption
	for _, o := range categoryOptions() {
		if o.Category == "chat" {
			chat = append(chat, o)
		}
	}
	if len(chat) != 2 {
		t.Fatalf("chat entries: %+v", chat)
	}
	if chat[0].Preset != "text" || chat[0].Fixed || chat[0].Label != "Text" {
		t.Errorf("the ordinary entry: %+v", chat[0])
	}
	if chat[1].Preset != config.VoicePreset || !chat[1].Fixed || chat[1].Label != "Voice Conversation" {
		t.Errorf("the voice entry: %+v", chat[1])
	}
	for _, o := range categoryOptions() {
		if o.Category != "chat" && o.Fixed {
			t.Errorf("only the voice entry has a fixed name: %+v", o)
		}
	}
}

func TestPresetLabelNamesTheVoicePresetApart(t *testing.T) {
	if presetLabel("voice-conversation", "chat") != "Voice Conversation" || presetLabel("xellter", "chat") != "Text" || presetLabel("voice-conversation", "images") != "Image" {
		t.Fatal("labels")
	}
}
