# Tool preferences

- For call-chain, impact, blast-radius, or cross-file/cross-service relationship questions (e.g. "what calls this", "what breaks if I change this", "trace this request across services"), query polyflow first — before grepping or spawning an Explore subagent. Polyflow answers graph-shaped questions that grep can't; grep/Explore remain fine for known-location lookups and simple string searches.
