# 079 — setup chooses the engine's API port before the engine starts

**Context.** `agent.json` records the local port VaultSync's sync engine answers on (`guiPort`). The engine's supervisor moves off that port when another program holds it as the engine starts, and records the new one. `setup` built its engine client from the port it read before the engine started. Where the engine was not running and another program had taken the recorded port, `setup` waited a minute at the old port and stopped with "the sync engine did not start", although the engine ran on the new one. This happened in two cases, both measured with the real engine:
- the engine's folder was moved aside, one of 077's ways out;
- the engine was stopped while another program took its port.

**Decision.**
1. **`setup` chooses the port before `prepare`.** It keeps the recorded port unless another program holds it while no `vaultsync run` holds the engine lock. In that case it takes a free loopback port. A fresh config gets that port, and `agent.json` records it. The engine then starts where `setup` waits, and the supervisor has nothing to move.
2. **A run's own engine keeps its port.** A run that holds the engine lock holds its engine's port itself, so a repeat `setup` on a running install changes nothing.
3. **The supervisor's move stays.** A run still moves off a port taken between two of its starts. Every reader of the port follows `agent.json`, as the control server does.

**Why.**
- `setup` must wait where the engine runs.
- Choosing the port before the start brings the engine and `setup` to the same port, with no step in between to follow.
- The engine lock tells a run's own engine from another program, without looking at the port's holder.

**Rejected alternatives.**
- **`setup` follows `agent.json` while it waits.** It would still wait at the old port until the supervisor had moved, and whatever answers there is not the engine.
- **The choice in `prepare` only.** It covers a fresh folder, not an engine folder whose engine was stopped while another program took its port.
- **Keeping the recorded port and saying so.** The person cannot free a port another program holds, and the engine works on any free loopback port.

**Not covered.**
- A record without a port beside an existing config: `prepare` reads the port from `config.xml` after the choice, and `setup` waits there as before.
- A run that holds the engine lock but has not bound its port yet, while another program holds the recorded one.
- The instant between the choice and the engine's bind.

**Links.** #413, #276; decision 077.
