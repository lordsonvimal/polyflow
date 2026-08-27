<!-- polyflow:nudge:start -->
## Tool preferences

- For call-chain, impact, blast-radius, or cross-file/cross-service relationship questions (e.g. "what calls this", "what breaks if I change this", "trace this request across services"), query polyflow first — before grepping or spawning an Explore subagent. Polyflow answers graph-shaped questions that grep can't; grep/Explore remain fine for known-location lookups and simple string searches.
- If the user's question bundles more than one ask (e.g. "trace flow X and tell me the impacts"), split it into one polyflow call per ask instead of pasting the whole sentence into a single query — a compound query dilutes the match. Strip conversational framing too; pass the core entity/feature name, not the full question.
- If a polyflow call returns empty or low-confidence results, don't fall back to grep/filesystem search yet — call resolve with the same term first to see ranked candidates (it often reveals the query just needs a different node type or service scope), then retry with that. If resolve itself comes back low-confidence, stop retrying with reworded variants of the same query (each retry costs a full call and rarely does better) — report what resolve found and ask, or fall back to grep once, not repeatedly.
- For a single "understand X" / "why does X happen" / "how does X flow end-to-end" question, call investigate first instead of manually sequencing search → resolve → context → trace yourself — it does that sequencing in one call and is cheaper than reconstructing it by hand.
<!-- polyflow:nudge:end -->
