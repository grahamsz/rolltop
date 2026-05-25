#!/usr/bin/env node

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [inputPath, outputDir = "test-results"] = process.argv.slice(2);

if (!inputPath) {
  console.error("usage: go-test-report.mjs <go-test-json> [output-dir]");
  process.exit(2);
}

mkdirSync(outputDir, { recursive: true });

const packages = new Map();

function packageRecord(name) {
  if (!packages.has(name)) {
    packages.set(name, {
      name,
      action: "run",
      elapsed: 0,
      output: [],
      tests: new Map()
    });
  }
  return packages.get(name);
}

function testRecord(pkg, name) {
  if (!pkg.tests.has(name)) {
    pkg.tests.set(name, {
      name,
      action: "run",
      elapsed: 0,
      output: []
    });
  }
  return pkg.tests.get(name);
}

function escapeXML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&apos;");
}

function cdata(value) {
  return String(value).replaceAll("]]>", "]]]]><![CDATA[>");
}

function formatSeconds(value) {
  const seconds = Number(value || 0);
  return seconds.toFixed(3);
}

function statusLabel(action) {
  switch (action) {
    case "pass":
      return "pass";
    case "fail":
      return "fail";
    case "skip":
      return "skip";
    default:
      return "unknown";
  }
}

const raw = readFileSync(inputPath, "utf8");
for (const line of raw.split(/\r?\n/)) {
  if (!line.trim()) continue;

  let event;
  try {
    event = JSON.parse(line);
  } catch {
    continue;
  }

  const pkg = packageRecord(event.Package || event.ImportPath || "unknown");
  if (event.Output) pkg.output.push(event.Output);
  if (event.Action === "build-output" && event.Output) pkg.output.push(event.Output);
  if (event.Action === "build-fail") pkg.action = "fail";

  if (event.Test) {
    const test = testRecord(pkg, event.Test);
    if (event.Output) test.output.push(event.Output);
    if (["pass", "fail", "skip"].includes(event.Action)) {
      test.action = event.Action;
      test.elapsed = Number(event.Elapsed || test.elapsed || 0);
    }
  } else if (["pass", "fail", "skip"].includes(event.Action)) {
    pkg.action = event.Action;
    pkg.elapsed = Number(event.Elapsed || pkg.elapsed || 0);
  }
}

const suites = [...packages.values()].sort((a, b) => a.name.localeCompare(b.name));
const summary = {
  packages: suites.length,
  tests: 0,
  failures: 0,
  skipped: 0,
  elapsed: suites.reduce((sum, suite) => sum + Number(suite.elapsed || 0), 0),
  failedPackages: []
};

function testCasesForSuite(suite) {
  if (suite.tests.size > 0) return [...suite.tests.values()].sort((a, b) => a.name.localeCompare(b.name));
  return [{
    name: "package",
    action: suite.action,
    elapsed: suite.elapsed,
    output: suite.output
  }];
}

const junitSuites = [];
const markdownRows = [];

for (const suite of suites) {
  const cases = testCasesForSuite(suite);
  const failures = cases.filter((test) => test.action === "fail").length;
  const skipped = cases.filter((test) => test.action === "skip").length;

  summary.tests += cases.length;
  summary.failures += failures;
  summary.skipped += skipped;
  if (suite.action === "fail" || failures > 0) summary.failedPackages.push(suite.name);

  const caseXML = cases.map((test) => {
    const output = test.output.join("");
    const attrs = `classname="${escapeXML(suite.name)}" name="${escapeXML(test.name)}" time="${formatSeconds(test.elapsed)}"`;
    if (test.action === "fail") {
      return `    <testcase ${attrs}>\n      <failure message="test failed"><![CDATA[${cdata(output || suite.output.join(""))}]]></failure>\n    </testcase>`;
    }
    if (test.action === "skip") {
      return `    <testcase ${attrs}>\n      <skipped />\n    </testcase>`;
    }
    return `    <testcase ${attrs} />`;
  }).join("\n");

  junitSuites.push([
    `  <testsuite name="${escapeXML(suite.name)}" tests="${cases.length}" failures="${failures}" skipped="${skipped}" time="${formatSeconds(suite.elapsed)}">`,
    caseXML,
    "  </testsuite>"
  ].join("\n"));

  markdownRows.push(`| ${statusLabel(suite.action)} | \`${suite.name}\` | ${cases.length} | ${failures} | ${skipped} | ${formatSeconds(suite.elapsed)}s |`);
}

const junit = [
  '<?xml version="1.0" encoding="UTF-8"?>',
  `<testsuites tests="${summary.tests}" failures="${summary.failures}" skipped="${summary.skipped}" time="${formatSeconds(summary.elapsed)}">`,
  ...junitSuites,
  "</testsuites>",
  ""
].join("\n");

const markdown = [
  "### Go Test Results",
  "",
  `Packages: ${summary.packages}`,
  `Tests: ${summary.tests}`,
  `Failures: ${summary.failures}`,
  `Skipped: ${summary.skipped}`,
  `Elapsed: ${formatSeconds(summary.elapsed)}s`,
  "",
  "| Status | Package | Tests | Failed | Skipped | Time |",
  "| --- | --- | ---: | ---: | ---: | ---: |",
  ...markdownRows,
  ""
].join("\n");

writeFileSync(join(outputDir, "go-test-junit.xml"), junit);
writeFileSync(join(outputDir, "go-test-report.md"), markdown);
writeFileSync(join(outputDir, "go-test-summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
