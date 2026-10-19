# The Go node keeps the audio heard under the wake chime (pre-roll on by default)

- **Date:** 2026-10-19 (loop #229, build agent; from the 2026-10-18 Pi 4 verification run)

The Python node records from after the wake chime (`history_secs=0`) and the Go node copied it,
flushing what the microphone captured while the chime played. On the Pi 4 that cost the first
syllables of a command said right after "hey Jarvis" ("Set a timer" heard as "It's at a timer",
4 of 6 runs), and the clipped transcripts ended `not_for_me`. The Go node now puts up to 1 s of
that audio in front of the recording by default (`--listen-preroll`, 0 restores the Python
behaviour). This is a deliberate departure from parity: endpointing, the silence threshold and
the follow-up gate's level reference are unchanged; only the uploaded command gains its start.
See [go-pi-node.md §3.12](../agent/go-pi-node.md).
