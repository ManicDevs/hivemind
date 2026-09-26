# OPENCODE ENVIRONMENT INSTRUCTIONS: 2-CONTINENT DEVELOPMENT SAFEGUARDS

You are modifying the active development environment for the serverless P2P mesh (Gaze and Hivemind). The environment is strictly limited to 2 continents (NA and EU). You must implement the following four options immediately to prevent host machine resource lockups.

## OPTION 1: Anti-Robbery Cryptographic Gate (Network Layer)
- Implement an Ed25519 signature validation wrapper on the network interface layer.
- Every incoming 'thought' or 'payload' frame broadcast must include a cryptographic signature header.
- If the signature cannot be validated against the authorized local development key pair, drop the packet instantly at the socket layer before it can consume any CPU processing or parsing cycles.

## OPTION 2: Xeon Protector Hardware-Aware Pacing (Daemon Layer)
- Modify the core peer gossip broadcast loop inside the daemon binary.
- Add an internal ticker that monitors local host resource metrics. 
- If the local CPU load exceeds 50%, dynamically adjust the network message pacing. Switch from a 50ms default interval to an exponential backoff (up to 1500ms delay per gossip burst).
- The binary must prioritize host system input responsiveness over mesh synchronization speed.

## OPTION 3: Split-Brain Hard-Coded Continental Gate (Consensus Layer)
- Hard-code a strict environment validator at the entry point of the cluster manager.
- Explicitly define an allowed map containing ONLY "na" and "eu".
- If any internal routing routine, file-system watcher, or incoming gossip node attempts to spin up or connect to an unauthorized continent (e.g., AS, SA, AF, OC, AN), force the process to log a fatal error and terminate instantly (`log.Fatalf`) before creating network interfaces.

## OPTION 4: Telemetry Ring Self-Reporting (Dashboard Interface Layer)
- Append a lightweight, 32-byte JSON metric object (`"vitals": {"cpu": float, "ram_bytes": int}`) to the tail end of every standard peer-to-peer message frame.
- Configure the Gaze dashboard frontend to drop into a throttled polling/static view mode when local processing overhead is high.
- Use these inline telemetry vitals to render the real-time resource halos around the active node icons on the global map interface.
