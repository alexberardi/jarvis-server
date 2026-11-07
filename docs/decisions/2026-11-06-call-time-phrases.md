# How a call reads spoken times and drafted availability

- **Date:** 2026-11-06 (agent, from the user's ask for a fully covered time suite)

Whisper transcripts of real calls kept producing phrasings the hand-written checks missed
("7.30", "I have 730", "six fifteen or seven thirty", "noon", offers with no weekday), and drafted
briefs said "Tomorrow at 7 p.m.". The time checks (`phone.judgeOffer` → `timewindow.CheckAll`) are
now proven by a generated matrix (`phone/time_phrases_test.go`). Where the speech is ambiguous,
the call decides as follows; anything not judged is left to the model, which still hears it.

Decided:

- **A bare number is a time only after a time word:** "at", "around", "about", "after",
  "before", "by", "say", "do" ("how about 8", "can do 8"), or "or" right after another time
  ("7 or 8"). "for 8", "we've got eight", "I have eight" are counts (a party, seats), and
  "eight" alone is nothing. Clock-shaped times ("7:30", "7pm", "7 o'clock", "7ish", "noon",
  "quarter to eight", "half seven") need no context. This holds with a weekday too: "Friday,
  table for 2" is no time (it used to be judged "Friday at 2").
- **Run-together clocks** ("730", "7 30") are times after an offer word ("do", "have", "got",
  "try", "about", "around", "at", "or", "say", "after", "before", "by"), or with am/pm ("730pm").
  Four digits ("1015") only after the offer words, not "at": "at 1200 Main Street" is an address.
  "for 730" is not read as a time.
- **"until 9" / "till 9" are not offers:** "we're full until 9" is a bound, not a time offered.
  "after 8" and "before 6" are judged at their boundary (after 8 against a 5-8pm window is out).
- **Each time takes the weekday said nearest to it** ("Friday is full; 7:30 on Saturday?" is
  Saturday). Weekdays in speech are whole words ("friend" isn't Friday; lower-case "sat", "sun",
  "wed", "mon" are English words).
- **No weekday:** the envelope's day when it has only one; none when it has several, or when the
  utterance names a day that isn't one weekday ("this weekend", "next week"). "Tomorrow",
  "tonight", "the day after tomorrow" and dates ("the 13th", "November 13") are resolved against
  the call's local start time.
- **"Same time Saturday"** has no clock, so it gets no verdict.
- **Drafted envelopes** also read "Mon–Fri 5-8pm", "Fri, Sat 6-9pm", "Friday or Saturday at 7pm",
  "this weekend", "weekdays", "after 6pm" / "6pm or later" (to midnight), "before 2pm" (from
  midnight), several Acceptable lines (one summary), Markdown bold and bullets, and times typed
  beside the "(fill in …)" placeholder. Dates inside an entry are not times. Part-of-day words
  ("afternoons") stay unparsed: a Python golden case pins that.
- Unchanged: a range is exact, a single time gets ±30 minutes (2026-11-06-call-time-grace), and
  `Check`/`ParseProposed` keep their Python golden behaviour.
