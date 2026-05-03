import { mutation, query } from "./_generated/server";
import { v } from "convex/values";

// Seeds the deterministic fixture row used by the Aster E2E. Returns
// the new doc's _id so the test can hand it to `getById`.
export const seedIan = mutation({
  args: {},
  handler: async (ctx) => {
    return await ctx.db.insert("messages", { name: "ian", body: "hello" });
  },
});

// The single query path the Aster broker has to serve for the v0.5
// E2E. Fires exactly one `1.0/get` async syscall — see
// npm-packages/convex/src/server/impl/database_impl.ts upstream and
// docs/CONVEX_POSTGRES_REFERENCE.md mapping in the Aster repo.
export const getById = query({
  args: { id: v.id("messages") },
  handler: async (ctx, { id }) => {
    return await ctx.db.get(id);
  },
});

// Optional second-stage path when you want to exercise an index-backed
// query rather than a point id lookup. Uncomment to flip the broker
// into the `1.0/queryStreamNext` path (not yet implemented in Aster).
//
// export const firstByName = query({
//   args: { name: v.string() },
//   handler: async (ctx, { name }) => {
//     return await ctx.db
//       .query("messages")
//       .withIndex("by_name", (q) => q.eq("name", name))
//       .first();
//   },
// });
