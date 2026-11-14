# A failed step check: wait and look again, then the model decides; risky steps are never redone unasked

- **Date:** 2026-11-13 (user; verified-replay Q2 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

A run checks each step's expectation. When the check fails:

1. It waits a short time and checks again without acting (a slow screen).
2. Still failing, the model looks at the screen with the step's goal and the workflow's goal and
   chooses: redo the step, reach the same result another way, or carry on because the result is
   there in another form.
3. It never redoes a step marked risky (sends, pays, deletes, shares) without asking the user:
   the step may have taken, and the expectation been wrong.
4. After a bounded number of attempts on one step it stops and asks, showing what it sees.

User: "A is fine. having a 'determined' model is probably the right approach."

Rejected: retrying the recorded step once and then asking (asks often, and a blind retry is the
double-send risk); treating step expectations as hints and checking only the end state (a wrong
turn at step 2 is found at step 9, after later steps acted on a wrong screen).

Strict or Creative applies to "another way": Strict stays within the recorded app and the same
kind of action; Creative may reach the result differently.
