const { test: base, expect } = require("@playwright/test");

// Exercise the embedded page in Chromium; HTTP-only checks cannot detect broken scripts.
const test = base.extend({
  page: async ({ page }, use) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("console", message => {
      if (message.type() === "error") errors.push(message.text());
    });
    await use(page);
    expect(errors, "browser console and script errors").toEqual([]);
  }
});

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    try {
      return (await request.get("/", { timeout: 1000 })).status();
    } catch {
      return 0;
    }
  }, { timeout: 25_000, intervals: [100, 250, 500] }).toBe(200);
});

test("the shipped server serves the public page and embedded assets with its page policy", async ({ request, page, context }) => {
  const response = await page.goto("/");
  const headers = await response.allHeaders();
  expect(response.status()).toBe(200);
  expect(headers["content-type"]).toContain("text/html");
  expect(headers["content-security-policy"]).toContain("script-src 'self'");
  expect(headers["x-content-type-options"]).toBe("nosniff");
  expect(headers["set-cookie"]).toBeUndefined();
  expect(await context.cookies()).toEqual([]);
  await expect(page.locator("#hero-title-dev")).toContainText("Keep your tools.");

  for (const [asset, contentType] of [
    ["landing.css", "text/css"],
    ["landing.js", "javascript"],
    ["brew-mark.svg", "image/svg+xml"]
  ]) {
    const assetResponse = await request.get("/static/" + asset);
    expect(assetResponse.status(), asset).toBe(200);
    expect(assetResponse.headers()["content-type"], asset).toContain(contentType);
  }
  expect((await request.get("/not-a-page")).status()).toBe(404);
});

test("admin routes require login and the bootstrapped account can sign in", async ({ request, page }) => {
  for (const route of ["/admin", "/admin/credentials", "/admin/requests"]) {
    const response = await request.get(route, { maxRedirects: 0 });
    expect(response.status(), route).toBe(303);
    expect(response.headers().location, route).toBe("/admin/login");
    expect(response.headers()["content-security-policy"], route).not.toContain("script-src");
  }
  await page.goto("/admin");
  await expect(page).toHaveURL(/\/admin\/login$/);
  await page.getByLabel("Username").fill("integration-admin");
  await page.getByLabel("Password").fill("integration-only-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByRole("button", { name: "Sign out · integration-admin" })).toBeVisible();
  await page.getByRole("button", { name: "Sign out · integration-admin" }).click();
  await page.goto("/admin");
  await expect(page).toHaveURL(/\/admin\/login$/);
});

test("perspective controls update the story, setup link and keyboard focus", async ({ page }) => {
  await page.goto("/");
  const dev = page.getByRole("tab", { name: "For developers" });
  const management = page.getByRole("tab", { name: "For management & IT" });
  await expect(dev).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#panel-dev")).toBeVisible();
  await expect(page.locator("#panel-management")).toBeHidden();

  await management.click();
  await expect(management).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#panel-management")).toBeVisible();
  await expect(page.locator("#panel-dev")).toBeHidden();
  await expect(page.locator("[data-copy='feature1Title']")).toHaveText("Choose the approved tools.");
  await expect(page.locator("#setup-cta")).toHaveAttribute("href", "/admin");
  await expect(page.locator("[data-setup='management']")).toBeVisible();

  await management.press("ArrowLeft");
  await expect(dev).toBeFocused();
  await expect(dev).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("[data-copy='feature1Title']")).toHaveText("Your terminal stays yours.");
  await expect(page.locator("#setup-cta")).toHaveAttribute("href", "#how-it-works");
  await expect(page.locator("[data-setup='management']")).toBeHidden();
  await dev.press("End");
  await expect(management).toBeFocused();
  await expect(management).toHaveAttribute("aria-selected", "true");
  await management.press("Home");
  await expect(dev).toBeFocused();
  await expect(dev).toHaveAttribute("aria-selected", "true");
});

test("the animated command pauses, resumes and replays the complete workflow", async ({ page }) => {
  await page.goto("/");
  const output = page.locator("#terminal-output");
  const pause = page.locator("#demo-pause");
  await expect(output).toContainText("brew");
  await pause.click();
  await expect(pause).toHaveAttribute("aria-label", "Resume terminal animation");
  const pausedOutput = await output.textContent();
  // Observe longer than the typing interval to ensure Pause stops active work.
  await page.waitForTimeout(300);
  expect(await output.textContent()).toBe(pausedOutput);
  await pause.click();
  await expect(output).toContainText("needs approval", { timeout: 5_000 });
  await page.getByRole("button", { name: "Replay terminal animation" }).click();
  await expect(output).not.toContainText("needs approval");
  await expect(output).toContainText("exact version verified", { timeout: 15_000 });
  await expect(page.locator("#terminal-stage")).toHaveText("Approved, installed, and verified");
  await expect(pause).toBeDisabled();
  await expect(output.locator(".terminal-line").filter({ hasText: /^❯ brew install node@22$/ })).toHaveCount(2);
});

test("reduced motion presents the complete demonstration without animated controls", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/");
  const output = page.locator("#terminal-output");
  await expect(output).toContainText("needs approval");
  await expect(output).toContainText("publishes a catalog grant");
  await expect(output).toContainText("exact version verified");
  await expect(page.locator("#terminal-stage")).toHaveText("Full demo · reduced motion");
  await expect(page.locator("#demo-pause")).toBeDisabled();
  await expect(output.locator(".typing")).toHaveCount(0);
  await page.getByRole("button", { name: "Replay terminal animation" }).click();
  await expect(output).toContainText("exact version verified");
});

test("both perspectives fit a mobile viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/");
  for (const name of ["For developers", "For management & IT"]) {
    await page.getByRole("tab", { name }).click();
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
  }
});

test("the Pages export works under its repository path and links to public self-hosting docs", async ({ page, request }) => {
  const baseURL = "http://127.0.0.1:4173";
  await page.emulateMedia({ reducedMotion: "reduce" });
  const response = await page.goto(baseURL + "/business-brew/");
  expect(response.status()).toBe(200);
  await expect(page.locator("#terminal-output")).toContainText("exact version verified");
  for (const brand of await page.getByRole("link", { name: "Business Brew home" }).all()) {
    await expect(brand).toHaveAttribute("href", "/business-brew/");
  }
  const resources = await page.locator("script[src], link[href], img[src]").evaluateAll(elements => elements.map(element => element.getAttribute("src") || element.getAttribute("href")));
  expect(resources.length).toBeGreaterThan(0);
  for (const resource of resources) {
    expect(resource).toMatch(/^\/business-brew\/static\//);
    expect((await request.get(baseURL + resource)).status(), resource).toBe(200);
  }

  await page.getByRole("tab", { name: "For management & IT" }).click();
  const managementURL = await page.locator("body").getAttribute("data-management-url");
  const docsURL = new URL(managementURL);
  expect(docsURL.origin).toBe("https://github.com");
  expect(docsURL.pathname).toBe("/hoermannpaul/business-brew");
  expect(docsURL.hash).toBe("#self-hosting");
  for (const selector of [".nav-admin", "#panel-management .hero-actions .primary", "#setup-cta", ".site-footer > a:last-child"]) {
    await expect(page.locator(selector)).toHaveAttribute("href", managementURL);
  }
  await expect(page.locator("#setup-cta")).toContainText("Read the self-hosting guide");
  await expect(page.locator("a[href='/admin']")).toHaveCount(0);
});
