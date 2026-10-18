// Enqueues a job on `isutori mock-portal` and waits for it to finish.
import { execFileSync, spawn } from "node:child_process";
import { chromium } from "playwright";

const addr = "127.0.0.1:17879";
execFileSync("go", ["build", "-o", ".cache/isutori", ".."], { stdio: "inherit" });
const portal = spawn(".cache/isutori", ["mock-portal", "--listen", addr, "--pending", "1s", "--running", "2s"], {
  stdio: "inherit",
});
const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  page.on("pageerror", (e) => {
    throw e;
  });
  await page.goto(`http://${addr}/contestant`);
  await page.locator("select").selectOption("1");
  await page.getByRole("button", { name: "Enqueue" }).click();
  await page.getByRole("heading", { name: "Job #1" }).waitFor();
  await page.getByText("Passed", { exact: true }).waitFor({ timeout: 15000 });
  console.log("smoke: OK");
} finally {
  await browser.close();
  portal.kill();
}
