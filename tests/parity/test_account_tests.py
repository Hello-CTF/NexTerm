from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("account_tests", ROOT / "scripts/parity/account_tests.py")
assert spec and spec.loader
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class AccountingTest(unittest.TestCase):
    def test_early_return_and_ignored_are_not_executed_passes(self):
        with tempfile.TemporaryDirectory(dir=ROOT / "testdata/parity") as directory:
            log = pathlib.Path(directory) / "sample.log"
            log.write_text(
                "test result: ok. 3 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\n"
                "[skip] no synthetic target\n"
                "test ssh_e2e ... ok\n"
                "test real_unit ... ok\n"
                "test live_server ... ignored, requires server\n"
                "test src-tauri/src/ipc_shim.rs - ipc_shim (line 11) ... ignored\n"
                "test result: ok. 1 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\n",
                encoding="utf-8",
            )
            result = module.account(log)
        self.assertEqual(result["runner_reported_passed"], 4)
        self.assertEqual(result["runner_reported_ignored"], 2)
        self.assertEqual(result["executed_passed_excluding_early_return_skips"], 3)
        self.assertEqual(result["early_return_skips"][0]["test"], "ssh_e2e")
        self.assertEqual(result["ignored_tests"][0]["test"], "live_server")


if __name__ == "__main__":
    unittest.main()
