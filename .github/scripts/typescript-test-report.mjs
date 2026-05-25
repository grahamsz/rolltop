#!/usr/bin/env node

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [statusValue, elapsedValue, logPath, outputDir = "test-results"] = process.argv.slice(2);

if (statusValue === undefined || elapsedValue === undefined || !logPath) {
  console.error("usage: typescript-test-report.mjs <status> <elapsed-seconds> <log-path> [output-dir]");
  process.exit(2);
}

mkdirSync(outputDir, { recursive: true });

const status = Number(statusValue);
const elapsed = Number(elapsedValue || 0);
const output = readFileSync(logPath, "utf8");
const passed = status === 0;

function cdata(value) {
  return String(value).replaceAll("]]>", "]]]]><![CDATA[>");
}

function formatSeconds(value) {
  const seconds = Number(value || 0);
  return seconds.toFixed(3);
}

const testcase = passed
  ? `    <testcase classname="typescript" name="tsc -b" time="${formatSeconds(elapsed)}" />`
  : [
      `    <testcase classname="typescript" name="tsc -b" time="${formatSeconds(elapsed)}">`,
      `      <failure message="TypeScript check failed"><![CDATA[${cdata(output)}]]></failure>`,
      "    </testcase>"
    ].join("\n");

const junit = [
  '<?xml version="1.0" encoding="UTF-8"?>',
  `<testsuites tests="1" failures="${passed ? 0 : 1}" skipped="0" time="${formatSeconds(elapsed)}">`,
  `  <testsuite name="typescript" tests="1" failures="${passed ? 0 : 1}" skipped="0" time="${formatSeconds(elapsed)}">`,
  testcase,
  "  </testsuite>",
  "</testsuites>",
  ""
].join("\n");

const markdown = [
  "### TypeScript Check Results",
  "",
  `Status: ${passed ? "pass" : "fail"}`,
  "Tests: 1",
  `Failures: ${passed ? 0 : 1}`,
  "Skipped: 0",
  `Elapsed: ${formatSeconds(elapsed)}s`,
  "",
  "| Status | Check | Tests | Failed | Skipped | Time |",
  "| --- | --- | ---: | ---: | ---: | ---: |",
  `| ${passed ? "pass" : "fail"} | \`tsc -b\` | 1 | ${passed ? 0 : 1} | 0 | ${formatSeconds(elapsed)}s |`,
  "",
  "Log: `test-results/typescript-typecheck.log`",
  ""
].join("\n");

const summary = {
  status: passed ? "pass" : "fail",
  tests: 1,
  failures: passed ? 0 : 1,
  skipped: 0,
  elapsed,
  log: logPath
};

writeFileSync(join(outputDir, "typescript-test-junit.xml"), junit);
writeFileSync(join(outputDir, "typescript-test-report.md"), markdown);
writeFileSync(join(outputDir, "typescript-test-summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
