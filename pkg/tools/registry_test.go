package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestFilteredClonesChannelHistoryToolContext(t *testing.T) {
	registry := NewToolRegistry()
	channelHistory := NewChannelHistoryTool()
	channelHistory.SetFetchCallback(func(channelID string, limit int, beforeID string) ([]ChannelHistoryMessage, error) {
		return []ChannelHistoryMessage{{
			ID:        "1",
			Author:    "tester",
			Content:   channelID,
			Timestamp: "now",
		}}, nil
	})
	registry.Register(channelHistory)

	filtered := registry.Filtered(ReadOnlyCompatibleTool)

	originalTool, ok := registry.Get("channel_history")
	if !ok {
		t.Fatal("expected channel_history in original registry")
	}
	filteredTool, ok := filtered.Get("channel_history")
	if !ok {
		t.Fatal("expected channel_history in filtered registry")
	}
	if originalTool == filteredTool {
		t.Fatal("expected filtered registry to clone channel_history tool instance")
	}

	originalHistory := originalTool.(*ChannelHistoryTool)
	filteredHistory := filteredTool.(*ChannelHistoryTool)
	originalHistory.SetContext("discord", "main-room")
	filteredHistory.SetContext("discord", "btw-room")

	originalResult := originalHistory.Execute(context.Background(), nil)
	if originalResult.IsError {
		t.Fatalf("original execute returned error: %s", originalResult.ForLLM)
	}
	if !strings.Contains(originalResult.ForLLM, "main-room") {
		t.Fatalf("expected original registry to retain main-room context, got: %s", originalResult.ForLLM)
	}

	filteredResult := filteredHistory.Execute(context.Background(), nil)
	if filteredResult.IsError {
		t.Fatalf("filtered execute returned error: %s", filteredResult.ForLLM)
	}
	if !strings.Contains(filteredResult.ForLLM, "btw-room") {
		t.Fatalf("expected filtered registry to retain btw-room context, got: %s", filteredResult.ForLLM)
	}
}

func TestRegistryOutputsAreSortedByToolName(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(NewWeatherForecastTool())
	registry.Register(NewWordCountTool(t.TempDir(), true))

	if got, want := registry.List(), []string{"weather_forecast", "word_count"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
	definitions := registry.ToProviderDefs()
	if got, want := []string{definitions[0].Function.Name, definitions[1].Function.Name}, registry.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider definition order = %v, want %v", got, want)
	}
	summaries := registry.GetSummaries()
	if !strings.Contains(summaries[0], "`weather_forecast`") || !strings.Contains(summaries[1], "`word_count`") {
		t.Fatalf("summary order = %v", summaries)
	}
}
