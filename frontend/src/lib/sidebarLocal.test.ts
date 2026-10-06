// @vitest-environment jsdom
import { beforeEach, expect, it } from "vitest";
import { clearOtherCollapsedAccounts, loadCollapsedAccounts, saveCollapsedAccounts } from "./sidebarLocal";

beforeEach(() => localStorage.clear());

it("keeps account collapse preferences isolated between users", () => {
  saveCollapsedAccounts(1, new Set(["account:7"]));
  saveCollapsedAccounts(2, new Set(["account:8"]));
  expect([...loadCollapsedAccounts(1)]).toEqual(["account:7"]);
  expect([...loadCollapsedAccounts(2)]).toEqual(["account:8"]);
  expect(loadCollapsedAccounts(3).size).toBe(0);
});

it("cleans up another user's preferences without touching other application data", () => {
  saveCollapsedAccounts(1, new Set(["account:7"]));
  saveCollapsedAccounts(2, new Set(["account:8"]));
  localStorage.setItem("unrelated", "keep");
  clearOtherCollapsedAccounts(2);
  expect(loadCollapsedAccounts(1).size).toBe(0);
  expect([...loadCollapsedAccounts(2)]).toEqual(["account:8"]);
  expect(localStorage.getItem("unrelated")).toBe("keep");
});
