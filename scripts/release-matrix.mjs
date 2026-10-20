#!/usr/bin/env node
import { desktopNativeMatrix } from "../.github/scripts/release-artifacts.mjs";

const include = desktopNativeMatrix();
if (include.length === 0) throw new Error("native matrix contains no desktop release targets");
process.stdout.write(`${JSON.stringify({ include })}\n`);
