import { beforeEach, describe, expect, it } from "vitest";
import {
  clearCollationCache,
  firstNumber,
  formatTableRef,
  knownCollation,
  listCollations,
  parseTableRef,
  tablesOf,
  traitKeys,
  traitsLabel,
  validCollationName,
} from "./collation";
import { COLLATIONS, info } from "./test/collation";
import { ENGINE, json, mockFetch, requestsTo, text } from "./test/http";

const LIST = "GET /api/v1/targets/dev-sql/collations";

beforeEach(() => clearCollationCache());

describe("traitsLabel", () => {
  it("writes the traits in the short form", () => {
    expect(traitsLabel(info("a"))).toBe("CI·AS");
    expect(traitsLabel(info("a", { caseSensitive: true, accentSensitive: false }))).toBe("CS·AI");
    expect(traitsLabel(info("a", { utf8: true }))).toBe("CI·AS·UTF8");
    expect(traitsLabel(info("a", { kanaSensitive: true, widthSensitive: true }))).toBe(
      "CI·AS·KS·WS",
    );
    expect(traitsLabel(info("a", { binary: true }))).toBe("BIN");
  });

  it("names the traits for the long form", () => {
    expect(traitKeys(info("a", { legacy: true, utf8: true }))).toEqual([
      "collation.trait.ci",
      "collation.trait.as",
      "collation.trait.utf8",
      "collation.trait.legacy",
    ]);
  });
});

describe("tables", () => {
  it("lists the distinct tables of some columns in order", () => {
    expect(
      tablesOf([
        { schema: "sales", table: "orders" },
        { schema: "dbo", table: "b" },
        { schema: "dbo", table: "a" },
        { schema: "dbo", table: "b" },
      ]),
    ).toEqual([
      { schema: "dbo", name: "a" },
      { schema: "dbo", name: "b" },
      { schema: "sales", name: "orders" },
    ]);
  });

  it("parses schema.table and [schema].[table]", () => {
    expect(parseTableRef("dbo.customers")).toEqual({ schema: "dbo", name: "customers" });
    expect(parseTableRef(" [my.schema].[a]]b] ")).toEqual({ schema: "my.schema", name: "a]b" });
    expect(parseTableRef("customers")).toBeNull();
    expect(parseTableRef("a.b.c")).toBeNull();
    expect(parseTableRef("[a].[b")).toBeNull();
    expect(parseTableRef("[a]")).toBeNull();
  });

  it("formats a table with ] doubled", () => {
    expect(formatTableRef({ schema: "dbo", name: "a]b" })).toBe("[dbo].[a]]b]");
  });
});

describe("names and numbers", () => {
  it("accepts what the engine accepts as a collation name", () => {
    expect(validCollationName("Latin1_General_100_CI_AS_SC_UTF8")).toBe(true);
    expect(validCollationName("1abc")).toBe(false);
    expect(validCollationName("a b")).toBe(false);
    expect(validCollationName("")).toBe(false);
  });

  it("reads the first number of a message", () => {
    expect(firstNumber("4 column(s) keep X")).toBe(4);
    expect(firstNumber(undefined)).toBe(0);
    expect(firstNumber("none")).toBe(0);
  });
});

describe("listCollations", () => {
  it("reads the list once per target and answers the next calls from memory", async () => {
    const fetch = mockFetch({ [LIST]: () => json({ collations: COLLATIONS }) });
    expect(await listCollations(ENGINE, "dev-sql")).toHaveLength(COLLATIONS.length);
    expect(await listCollations(ENGINE, "dev-sql")).toHaveLength(COLLATIONS.length);
    expect(requestsTo(fetch, LIST)).toHaveLength(1);
    expect(knownCollation(ENGINE, "dev-sql", "latin1_general_100_ci_as")).toBe(true);
    expect(knownCollation(ENGINE, "dev-sql", "Nope")).toBe(false);
  });

  it("does not know before it is read", () => {
    expect(knownCollation(ENGINE, "dev-sql", "x")).toBeUndefined();
  });

  it("reads again when the password epoch changes", async () => {
    const fetch = mockFetch({ [LIST]: () => json({ collations: COLLATIONS }) });
    await listCollations(ENGINE, "dev-sql", undefined, 0);
    await listCollations(ENGINE, "dev-sql", undefined, 1);
    expect(requestsTo(fetch, LIST)).toHaveLength(2);
  });

  it("does not keep a failed read", async () => {
    let call = 0;
    const fetch = mockFetch({
      [LIST]: () => (call++ === 0 ? text("boom\n", 502) : json({ collations: COLLATIONS })),
    });
    await expect(listCollations(ENGINE, "dev-sql")).rejects.toThrow("boom");
    expect(await listCollations(ENGINE, "dev-sql")).toHaveLength(COLLATIONS.length);
    expect(requestsTo(fetch, LIST)).toHaveLength(2);
  });

  it("stops waiting when the caller leaves, and keeps the read", async () => {
    const fetch = mockFetch({ [LIST]: () => json({ collations: COLLATIONS }) });
    const controller = new AbortController();
    controller.abort();
    await expect(listCollations(ENGINE, "dev-sql", controller.signal)).rejects.toThrow();
    expect(await listCollations(ENGINE, "dev-sql")).toHaveLength(COLLATIONS.length);
    expect(requestsTo(fetch, LIST)).toHaveLength(1);
  });
});
