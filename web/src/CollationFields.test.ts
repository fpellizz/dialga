import { describe, expect, it } from "vitest";
import { collationComplete } from "./CollationFields";

describe("collationComplete", () => {
  it("takes the database default when no other collation is selected", () => {
    expect(collationComplete("convert-columns", { database: "sales" }, undefined)).toBe(true);
  });

  it("is incomplete with Another collation selected and an empty picker", () => {
    expect(collationComplete("convert-columns", { database: "sales" }, undefined, true)).toBe(
      false,
    );
    expect(
      collationComplete(
        "convert-columns",
        { database: "sales", collation: "Latin1_General_BIN2" },
        true,
        true,
      ),
    ).toBe(true);
  });

  it("still needs a name for a new default collation", () => {
    expect(collationComplete("set-collation", { database: "sales" }, undefined)).toBe(false);
  });
});
