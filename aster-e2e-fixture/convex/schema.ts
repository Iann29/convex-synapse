import { defineSchema, defineTable } from "convex/server";
import { v } from "convex/values";

// Aster E2E fixture schema. Intentionally one table, two fields, one
// index — the smallest non-trivial shape that exercises both the pure
// `db.get(id)` path and the index-backed query path. Drop `by_name`
// once the broker is happy with `1.0/get` if you want to narrow the
// minimum bar.
export default defineSchema({
  messages: defineTable({
    name: v.string(),
    body: v.string(),
  }).index("by_name", ["name"]),
});
