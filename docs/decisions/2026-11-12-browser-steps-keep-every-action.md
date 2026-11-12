# Learning a browser workflow keeps every action of a step

- **Date:** 2026-11-12 (agent, found while checking the workflow-skills learn prompt on the dev
  box's model; refines [2026-10-19-learning-workflows.md](2026-10-19-learning-workflows.md))

A browser step's action was "the last action among its events". The dev box's background model
groups a sign-in as one step (open the page, the username, the password, the button), with the
old prompt and the new one alike, so the learnt workflow pressed Sign in on an empty form: the
typing was gone. The scripted model of the tests answers one event per step, which hid it.

Now every action of a step (a click, typing, a key) becomes its own step with the step's intent
and goal, as desktop recordings have done since #581
([2026-10-24-desktop-replay-opens-apps.md](2026-10-24-desktop-replay-opens-apps.md)). A step with
no action keeps its first navigation, as before. The step's expectation goes on its last action;
an action that opened a page expects that page.
