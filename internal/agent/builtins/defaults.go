package builtins

import "encoding/json"

// defaultRoutines are RoutineCommand._default_routines (nodesettings.DefaultRoutines are their
// slugs), as JSON so they decode exactly as a stored routine does.
const defaultRoutines = `{
  "good_morning": {
    "trigger_phrases": ["good morning", "morning routine", "start my day"],
    "steps": [
      {"command": "control_device", "args": {"floor": "Downstairs", "action": "turn_on"}, "label": "lights"},
      {"command": "get_weather", "args": {"resolved_datetimes": ["today"]}, "label": "weather"},
      {"command": "get_calendar_events", "args": {"resolved_datetimes": ["today"]}, "label": "calendar"}
    ],
    "response_instruction": "Give a cheerful morning briefing with weather and calendar highlights."
  },
  "good_night": {
    "trigger_phrases": ["good night", "bedtime", "going to bed", "time for bed"],
    "steps": [
      {"command": "control_device", "args": {"floor": "Downstairs", "action": "turn_off"}, "label": "lights"},
      {"command": "get_calendar_events", "args": {"resolved_datetimes": ["tomorrow"]}, "label": "tomorrow"}
    ],
    "response_instruction": "Brief goodnight with tomorrow's first appointment if any."
  },
  "morning_briefing": {
    "type": "briefing",
    "response_length": "medium",
    "trigger_phrases": ["morning briefing", "daily briefing", "give me my briefing", "what's happening today", "catch me up", "daily update"],
    "steps": [
      {"command": "get_weather", "args": {"resolved_datetimes": ["today"]}, "label": "weather"},
      {"command": "get_calendar_events", "args": {"resolved_datetimes": ["today"]}, "label": "calendar"},
      {"command": "get_news", "args": {"category": "general", "count": 3}, "label": "news"}
    ],
    "response_instruction": "Deliver a morning briefing in a natural, flowing narrative style. Start with today's weather, then mention calendar events, then summarize the top news headlines. Sound like a personal news anchor, not a list of bullet points."
  },
  "nightly_briefing": {
    "type": "briefing",
    "response_length": "medium",
    "trigger_phrases": ["nightly briefing", "evening briefing", "nightly update", "evening update", "what happened today", "end of day briefing"],
    "steps": [
      {"command": "get_weather", "args": {"resolved_datetimes": ["tomorrow"]}, "label": "tomorrow_weather"},
      {"command": "get_calendar_events", "args": {"resolved_datetimes": ["tomorrow"]}, "label": "tomorrow_calendar"},
      {"command": "get_news", "args": {"category": "general", "count": 3}, "label": "news"}
    ],
    "response_instruction": "Deliver an evening briefing in a calm, winding-down tone. Start with tomorrow's weather outlook, then mention any calendar events for tomorrow, then summarize today's top news headlines. Keep it relaxed and conversational."
  }
}`

// DefaultRoutines are the built-in routines, a fresh copy each call.
func DefaultRoutines() map[string]map[string]any {
	var out map[string]map[string]any
	if err := json.Unmarshal([]byte(defaultRoutines), &out); err != nil {
		panic(err)
	}
	return out
}
