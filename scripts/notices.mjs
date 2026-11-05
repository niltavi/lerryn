#!/usr/bin/env node
// Generates THIRD_PARTY_NOTICES for what a Lerryn release distributes: the
// Go modules linked into the lerryn binary (server, runner and CLI, for every
// release platform) and the npm packages bundled into the web console that
// the binary embeds. The mobile app is distributed separately and carries its
// own notices; dev and test dependencies are not distributed.
//
//   node scripts/notices.mjs          rewrite THIRD_PARTY_NOTICES
//   node scripts/notices.mjs --check  fail if the file is out of date
//
// Both modes fail when a dependency's license cannot be identified or is not
// on the allowlist below. Extending the allowlist is a licensing decision and
// goes through human review (AGENTS.md).

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, realpathSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const OUTPUT = join(ROOT, "THIRD_PARTY_NOTICES");
const MAIN_PACKAGE = "./server/cmd/lerryn";
const PLATFORMS = ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"];
const WEB_APP = join(ROOT, "apps/web");

// Licenses that may be combined with an AGPL-3.0 work and distributed with
// it. Every one of them is also compatible with the Apache-2.0 parts of the
// repository (runtimes/sdk/, skills/), which today have no third-party code.
const ALLOWED = new Set([
  "0BSD",
  "Apache-2.0",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "CC0-1.0",
  "ISC",
  "MIT",
  "MPL-2.0",
  "OFL-1.1",
  "Unlicense",
  "LicenseRef-Public-Domain",
]);

// Packages that are on the dependency graph but contribute nothing to the
// built bundle: type declarations are erased by the TypeScript compiler.
const NOT_BUNDLED = [/^@types\//];

// Build tooling that injects its own runtime helpers into the bundle.
const BUNDLER_RUNTIME = ["vite"];

const LICENSE_FILE = /^(licen[cs]e|copying|unlicense|copyright)([-._][^.]*)?(\.(md|txt|docs))?$/i;
const NOTICE_FILE = /^notice([-._][^.]*)?(\.(md|txt))?$/i;

// License files whose license is decided here instead of by the classifier:
// files that cover material not linked into the binary, and compound files
// that reproduce many licenses. Each decision is a licensing review item.
const FILE_DECISIONS = {
  "github.com/opencontainers/go-digest": {
    "LICENSE.docs": { skip: "CC-BY-SA-4.0 for the documentation, which is not part of the binary" },
  },
  "modernc.org/memory": {
    "LICENSE-LOGO": { skip: "attribution for the project logo, which is not part of the binary" },
  },
  "modernc.org/sqlite": {
    // Section 1 of the file lists what is linked: Go modules listed here on
    // their own, SQLite (public domain), sqlite-vec (MIT, linked only when
    // modernc.org/sqlite/vec is imported, which Lerryn does not).
    "LICENSE-3RD-PARTY.md": { license: ["BSD-3-Clause", "LicenseRef-Public-Domain", "MIT"] },
  },
  "modernc.org/libc": {
    // musl libc, go-netdb, NixOS/nixpkgs (MIT) and Go (BSD-3-Clause).
    "LICENSE-3RD-PARTY.md": { license: ["BSD-3-Clause", "MIT"] },
  },
};

// Identifies license texts by phrases that only that license uses. A file
// may contain several licenses; all matches are reported.
const CLASSIFIERS = [
  ["Apache-2.0", [/apache license,? version 2\.0|apache license\s+version 2\.0/i]],
  ["MPL-2.0", [/mozilla public license,? version 2\.0/i]],
  ["Unlicense", [/this is free and unencumbered software released into the public domain/i]],
  ["LicenseRef-Public-Domain", [/sqlite is public domain/i]],
  ["CC0-1.0", [/cc0 1\.0 universal/i]],
  ["OFL-1.1", [/sil open font license/i, /version 1\.1/i]],
  ["0BSD", [/permission to use, copy, modify, and\/or distribute this software for any purpose with or without fee is hereby granted\. the software is provided/i]],
  ["ISC", [/permission to use, copy, modify, and\/?(or)? distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies/i]],
  ["MIT", [/permission is hereby granted, free of charge, to any person obtaining a copy/i]],
  ["BSD-3-Clause", [/redistribution and use in source and binary forms/i, /(neither the name|the names? of (its|the) (copyright holders? nor|contributors|authors))/i]],
  ["BSD-2-Clause", [/redistribution and use in source and binary forms/i]],
];

function classify(text) {
  const t = text.replace(/\s+/g, " ");
  const ids = CLASSIFIERS.filter(([, patterns]) => patterns.every((p) => p.test(t))).map(([id]) => id);
  // The BSD-2-Clause phrases are a subset of BSD-3-Clause.
  return ids.includes("BSD-3-Clause") ? ids.filter((id) => id !== "BSD-2-Clause") : ids;
}

function run(cmd, args, env = {}) {
  return execFileSync(cmd, args, {
    cwd: ROOT,
    encoding: "utf8",
    env: { ...process.env, ...env },
    maxBuffer: 64 << 20,
  });
}

const usedDecisions = new Set();

function readNoticeFiles(dir, packageName) {
  const licenses = [];
  const notices = [];
  const skipped = [];
  const decisions = FILE_DECISIONS[packageName] ?? {};
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (!statSync(path).isFile()) continue;
    const decision = decisions[name];
    if (decision) usedDecisions.add(`${packageName}/${name}`);
    if (decision?.skip) {
      skipped.push({ name, reason: decision.skip });
    } else if (LICENSE_FILE.test(name)) {
      const text = readFileSync(path, "utf8");
      licenses.push({ name, text, ids: decision?.license ?? classify(text) });
    } else if (NOTICE_FILE.test(name)) {
      notices.push({ name, text: readFileSync(path, "utf8") });
    }
  }
  return { licenses, notices, skipped };
}

// --- Go modules ----------------------------------------------------------

function goModules() {
  const modules = new Map();
  const format = "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}";
  for (const platform of PLATFORMS) {
    const [goos, goarch] = platform.split("/");
    const out = run("go", ["list", "-deps", "-f", format, MAIN_PACKAGE], { GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0" });
    for (const line of out.split("\n")) {
      if (!line.trim()) continue;
      const [path, version, dir] = line.split("\t");
      if (!dir) throw new Error(`go module ${path}@${version} is not in the module cache; run go mod download`);
      modules.set(`${path}@${version}`, { name: path, version, dir });
    }
  }
  return [...modules.values()].map((m) => ({
    kind: "go",
    name: m.name,
    version: m.version,
    source: `https://pkg.go.dev/${m.name}@${m.version}`,
    declared: null,
    ...readNoticeFiles(m.dir, m.name),
  }));
}

// The Go distribution's LICENSE is missing from some GOROOTs (Homebrew omits
// it), and the output must not depend on the machine that generates it, so
// the text is kept here. golang.org/x modules carry the same text.
const GO_LICENSE = `Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
`;

function goStandardLibrary() {
  return {
    kind: "go",
    name: "Go standard library and runtime",
    version: "",
    source: "https://go.dev/",
    declared: null,
    licenses: [{ name: "LICENSE", text: GO_LICENSE, ids: ["BSD-3-Clause"] }],
    notices: [],
    skipped: [],
  };
}

// --- npm packages --------------------------------------------------------

function resolvePackage(name, fromDir) {
  for (let dir = fromDir; ; dir = dirname(dir)) {
    const candidate = join(dir, "node_modules", name, "package.json");
    if (existsSync(candidate)) return dirname(realpathSync(candidate));
    if (dirname(dir) === dir) return null;
  }
}

function isWorkspace(dir) {
  return !relative(ROOT, dir).split(sep).includes("node_modules");
}

// Walks the runtime dependency closure of the web console: dependencies,
// optional dependencies and non-optional peers, through workspace packages.
function npmPackages() {
  const seen = new Map();
  const visited = new Set();
  const visit = (dir, required) => {
    if (visited.has(dir)) return;
    visited.add(dir);
    const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
    if (!isWorkspace(dir)) {
      if (NOT_BUNDLED.some((re) => re.test(pkg.name))) return;
      seen.set(`${pkg.name}@${pkg.version}`, { dir, pkg });
    }
    const peersMeta = pkg.peerDependenciesMeta ?? {};
    const deps = [
      ...Object.keys(pkg.dependencies ?? {}).map((n) => [n, true]),
      ...Object.keys(pkg.optionalDependencies ?? {}).map((n) => [n, false]),
      ...Object.keys(pkg.peerDependencies ?? {})
        .filter((n) => !peersMeta[n]?.optional)
        .map((n) => [n, false]),
    ];
    for (const [name, mustResolve] of deps) {
      const child = resolvePackage(name, dir);
      if (!child) {
        if (mustResolve && required) throw new Error(`${pkg.name}: dependency ${name} is not installed; run pnpm install`);
        continue;
      }
      visit(child, mustResolve && required);
    }
  };
  visit(WEB_APP, true);
  for (const name of BUNDLER_RUNTIME) {
    const dir = resolvePackage(name, WEB_APP);
    if (!dir) throw new Error(`${name} is not installed; run pnpm install`);
    const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
    seen.set(`${pkg.name}@${pkg.version}`, { dir, pkg });
  }
  return [...seen.values()].map(({ dir, pkg }) => ({
    kind: "npm",
    name: pkg.name,
    version: pkg.version,
    source: `https://www.npmjs.com/package/${pkg.name}/v/${pkg.version}`,
    declared: typeof pkg.license === "string" ? pkg.license : (pkg.license?.type ?? null),
    ...readNoticeFiles(dir, pkg.name),
  }));
}

// --- Rendering -----------------------------------------------------------

const SPDX_ID = /[A-Za-z0-9.+-]+/g;

function licenseOf(entry, problems) {
  const label = `${entry.name}${entry.version ? `@${entry.version}` : ""}`;
  for (const l of entry.licenses) {
    if (l.ids.length === 0) problems.push(`${label}: could not identify the license in ${l.name}`);
  }
  let ids = [...new Set(entry.licenses.flatMap((l) => l.ids))].sort();
  let expression = ids.join(" AND ");
  if (entry.declared) {
    const declaredIds = (entry.declared.match(SPDX_ID) ?? []).filter((t) => !["AND", "OR", "WITH"].includes(t));
    if (entry.licenses.length === 0) {
      // A few npm packages ship no license file; the package.json field is
      // then the only statement of the license.
      ids = declaredIds;
      expression = `${entry.declared} (declared in package.json; the package has no license file)`;
    } else if (ids.every((id) => declaredIds.includes(id))) {
      // The SPDX expression says whether several licenses are alternatives.
      expression = entry.declared;
    } else {
      // The license files win; the disagreement is worth a reviewer's look.
      expression = `${expression} (package.json declares ${entry.declared})`;
    }
  } else if (entry.licenses.length === 0) {
    problems.push(`${label}: no license file found`);
  }
  for (const id of ids) {
    if (!ALLOWED.has(id)) problems.push(`${label}: license ${id} is not on the allowlist in scripts/notices.mjs`);
  }
  return expression || "UNKNOWN";
}

function normalize(text) {
  return text.replace(/\r\n?/g, "\n").replace(/[ \t]+$/gm, "").trim();
}

function render() {
  const problems = [];
  const sortByName = (a, b) => a.name.localeCompare(b.name, "en") || a.version.localeCompare(b.version, "en");
  const goEntries = [goStandardLibrary(), ...goModules().sort(sortByName)];
  const npmEntries = npmPackages().sort(sortByName);

  // Identical license texts (the Apache-2.0 text, most often) are printed once.
  const texts = new Map();
  const textId = (text) => {
    const body = normalize(text);
    const hash = createHash("sha256").update(body).digest("hex");
    if (!texts.has(hash)) texts.set(hash, { id: `T${texts.size + 1}`, body });
    return texts.get(hash).id;
  };

  const rows = (entries) =>
    entries.map((e) => ({
      ...e,
      license: licenseOf(e, problems),
      textIds: e.licenses.map((l) => `${l.name}: ${textId(l.text)}`),
      noticeIds: e.notices.map((n) => `${n.name}: ${textId(n.text)}`),
    }));
  const goRows = rows(goEntries);
  const npmRows = rows(npmEntries);
  for (const [pkg, files] of Object.entries(FILE_DECISIONS)) {
    for (const file of Object.keys(files)) {
      if (!usedDecisions.has(`${pkg}/${file}`)) problems.push(`FILE_DECISIONS: ${pkg}/${file} no longer exists; remove the entry`);
    }
  }

  const lines = [];
  const rule = "=".repeat(78);
  lines.push(
    "Code generated by scripts/notices.mjs (make notices). DO NOT EDIT.",
    "",
    "Lerryn third-party notices",
    "",
    "Lerryn is licensed under AGPL-3.0-only; runtimes/sdk/ and skills/ are",
    "licensed under Apache-2.0. A Lerryn release distributes the third-party",
    "software listed below, each under its own license.",
    "",
    "  - The lerryn binary (server, runner, all-in-one and CLI) for linux and",
    "    darwin on amd64 and arm64 links the Go modules in part 1.",
    "  - The web console embedded in the binary bundles the npm packages in",
    "    part 2, including the IBM Plex Mono and DM Sans fonts (OFL-1.1).",
    "  - The introduction website also distributes the unmodified DM Sans",
    "    fonts listed in part 2 and Cormorant Garamond 5.3.0 (Copyright 2015",
    "    The Cormorant Project Authors, OFL-1.1, from @fontsource); both OFL",
    "    texts are included with its font assets.",
    "  - The shared Niltavi theme in part 3 is vendored in design/niltavi-ui.",
    "  - The container image is based on gcr.io/distroless/static-debian12,",
    "    whose Debian packages keep their copyright files under",
    "    /usr/share/doc/ inside the image.",
    "",
    "The mobile app is distributed separately with its own notices. Runtime",
    "images that runners pull for tasks (Python, Node, Shell) are not part of",
    "a Lerryn release.",
    "",
    "Each entry names its license files and the license text below that",
    "reproduces them; identical texts are printed once.",
    "",
  );
  const section = (title, entries) => {
    lines.push(rule, title, rule, "");
    for (const e of entries) {
      lines.push(`${e.name}${e.version ? ` ${e.version}` : ""}`);
      lines.push(`  License: ${e.license}`);
      lines.push(`  Source:  ${e.source}`);
      for (const t of e.textIds) lines.push(`  License text: ${t}`);
      for (const t of e.noticeIds) lines.push(`  Notice: ${t}`);
      for (const s of e.skipped) lines.push(`  Not reproduced: ${s.name} (${s.reason})`);
      lines.push("");
    }
  };
  section(`Part 1. Go modules (${goRows.length})`, goRows);
  section(`Part 2. npm packages in the web console (${npmRows.length})`, npmRows);
  const themeRows = rows([{
    name: "niltavi-ui",
    version: "070e63c3be6ae6807ab907c9d1ca2614993614f7",
    source: "https://github.com/niltavi/niltavi-ui/tree/070e63c3be6ae6807ab907c9d1ca2614993614f7",
    declared: "MIT",
    ...readNoticeFiles(join(ROOT, "design/niltavi-ui"), "niltavi-ui"),
  }]);
  section(`Part 3. Vendored themes (${themeRows.length})`, themeRows);
  lines.push(rule, `Part 4. License and notice texts (${texts.size})`, rule, "");
  for (const { id, body } of texts.values()) {
    lines.push(`----- ${id} -----`, "", body, "");
  }
  return { content: `${lines.join("\n").trimEnd()}\n`, problems };
}

const { content, problems } = render();
if (problems.length > 0) {
  console.error("third-party notices: license problems");
  for (const p of problems) console.error(`  ${p}`);
  process.exit(1);
}
if (process.argv.includes("--check")) {
  const current = existsSync(OUTPUT) ? readFileSync(OUTPUT, "utf8") : "";
  if (current !== content) {
    console.error("THIRD_PARTY_NOTICES is out of date with the Go and npm dependencies; run make notices and commit the result");
    process.exit(1);
  }
  console.log("THIRD_PARTY_NOTICES is up to date");
} else {
  writeFileSync(OUTPUT, content);
  console.log(`wrote ${relative(ROOT, OUTPUT)}`);
}
