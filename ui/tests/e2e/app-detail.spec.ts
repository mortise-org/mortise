import { expect, test } from "@playwright/test";
import {
  ensureAdmin,
  loginViaAPI,
  injectToken,
  randomSuffix,
  createProjectViaAPI,
  createAppViaAPI,
  deleteProjectViaAPI,
  deleteAppViaAPI,
} from "./helpers";

// End-to-end tests for the App drawer (/projects/{project}/apps/{app}).
//
// The new UI renders a sliding drawer overlay (45% width from the right) on
// top of the canvas. The drawer has five tabs: Deployments, Variables, Logs,
// Metrics, Settings.

test.describe("app drawer detail", () => {
  let adminToken: string;
  let projectName: string;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    adminToken = await loginViaAPI(request);
    projectName = `e2e-appdet-${randomSuffix()}`;
    await createProjectViaAPI(
      request,
      adminToken,
      projectName,
      "App detail E2E tests",
    );
  });

  test.afterAll(async ({ request }) => {
    await deleteProjectViaAPI(request, adminToken, projectName);
  });

  test("drawer structure - breadcrumbs, heading, phase badge, five tabs", async ({
    page,
    request,
  }) => {
    const appName = `e2e-struct-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);

    // Breadcrumb in toolbar: "Projects" link and project name.
    await expect(page.getByRole("link", { name: "Projects" })).toBeVisible({
      timeout: 10_000,
    });
    await expect(
      page.getByRole("link", { name: projectName }),
    ).toBeVisible();

    // Drawer heading with app name.
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible();

    // Phase badge (any phase is fine).
    await expect(
      page.getByText(/Pending|Building|Deploying|Ready|Failed/).first(),
    ).toBeVisible();

    // Close button.
    await expect(
      page.getByRole("button", { name: "Close drawer" }),
    ).toBeVisible();

    // All five drawer tabs.
    await expect(
      page.getByRole("button", { name: "Deployments" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Variables" }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Logs" })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Metrics" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Settings", exact: true }),
    ).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("close drawer navigates back to project canvas", async ({
    page,
    request,
  }) => {
    const appName = `e2e-close-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Close drawer" }).click();

    await expect(page).toHaveURL(`/projects/${projectName}`, {
      timeout: 5_000,
    });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Variables tab - shows empty state and New variable button", async ({
    page,
    request,
  }) => {
    const appName = `e2e-vars-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Variables" }).click();

    // Empty state — actual text is "No variables set. Click..."
    await expect(page.getByText(/No variables set/)).toBeVisible({
      timeout: 5_000,
    });

    // "New variable" button.
    await expect(
      page.getByRole("button", { name: "New variable", exact: true }),
    ).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Variables tab - add a variable inline", async ({ page, request }) => {
    const appName = `e2e-addvar-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Variables" }).click();

    // Click "New variable" to show inline form.
    await page.getByRole("button", { name: "New variable", exact: true }).click();

    // VARIABLE_NAME and value inputs appear.
    await expect(page.getByPlaceholder("VARIABLE_NAME")).toBeVisible();
    await expect(page.getByPlaceholder("value or binding ref")).toBeVisible();

    // Fill key/value.
    await page.getByPlaceholder("VARIABLE_NAME").fill("MY_VAR");
    await page.getByPlaceholder("value or binding ref").fill("my_value");

    // Cancel to discard without saving.
    await page.getByRole("button", { name: "Cancel" }).last().click();

    // Empty state should reappear.
    await expect(page.getByText(/No variables set/)).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Variables tab - Raw/Import mode shows textarea", async ({
    page,
    request,
  }) => {
    const appName = `e2e-rawvar-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Variables" }).click();

    // Click "Raw / Import" to switch modes.
    await page.getByRole("button", { name: "Raw / Import" }).click();

    // Textarea with placeholder should appear (e.g. DATABASE_URL=postgres://...).
    const textarea = page.getByPlaceholder(/DATABASE_URL/);
    await expect(textarea).toBeVisible();

    // Cancel returns to form view.
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByText(/No variables set/)).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Settings tab - Source section and Danger Zone visible", async ({
    page,
    request,
  }) => {
    const appName = `e2e-setsrc-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Settings", exact: true }).click();

    // Filter input.
    await expect(page.getByPlaceholder("Filter settings…")).toBeVisible();

    // Source section heading.
    await expect(page.getByRole("heading", { name: "Source" })).toBeVisible();

    // Networking section.
    await expect(page.getByRole("heading", { name: "Networking" })).toBeVisible();

    // Danger Zone section.
    await expect(page.getByText("Danger Zone")).toBeVisible();
    // Initial "Delete" button (before confirmation).
    await expect(page.getByRole("button", { name: "Delete", exact: true })).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Settings tab - delete app flow requires name confirmation then redirects", async ({
    page,
    request,
  }) => {
    const appName = `e2e-delapp-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Settings", exact: true }).click();

    // Click "Delete" button in danger zone.
    await page.getByRole("button", { name: "Delete", exact: true }).click();

    // Confirmation UI: "Type [appName] to confirm deletion."
    await expect(
      page.getByText(/to confirm deletion/),
    ).toBeVisible();

    const confirmInput = page.getByPlaceholder(appName);
    await confirmInput.fill(appName);

    // "Delete App" button should now be enabled.
    const deleteAppBtn = page.getByRole("button", { name: "Delete App" });
    await expect(deleteAppBtn).toBeEnabled();
    await deleteAppBtn.click();

    // Should redirect back to project canvas.
    await expect(page).toHaveURL(`/projects/${projectName}`, {
      timeout: 10_000,
    });
  });

  test("Settings tab - Domains section shows add input", async ({
    page,
    request,
  }) => {
    const appName = `e2e-domains-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Settings", exact: true }).click();

    // Domains section has an input for adding custom domains.
    const domainInput = page.getByPlaceholder("custom.example.com");
    await expect(domainInput).toBeVisible({ timeout: 5_000 });

    // Intercept the add domain API call.
    const customDomain = "custom.test.example.com";
    await page.route(`**/api/projects/*/apps/*/domains*`, (route) => {
      if (route.request().method() === "POST") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ primary: null, custom: [customDomain] }),
        });
      }
      return route.continue();
    });

    await domainInput.fill(customDomain);
    await page.getByRole("button", { name: "Add", exact: true }).click();

    // Custom domain should appear.
    await expect(page.getByText(customDomain)).toBeVisible({ timeout: 5_000 });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Logs tab - renders log area", async ({ page, request }) => {
    const appName = `e2e-logs-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole("button", { name: "Logs", exact: true }).click();

    // Logs tab renders the log viewer.
    // Live tail toggle.
    await expect(page.getByText("Live tail")).toBeVisible({ timeout: 5_000 });

    // Clear button.
    await expect(page.getByRole("button", { name: "Clear" })).toBeVisible();

    // Log container (even if empty, shows placeholder text).
    await expect(
      page.getByText("No logs yet…").or(page.locator(".font-mono.text-xs")),
    ).toBeVisible({ timeout: 5_000 });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("Deployments tab - shows no deployments or deploy state", async ({
    page,
    request,
  }) => {
    const appName = `e2e-deploys-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(
      page.getByRole("heading", { name: appName }),
    ).toBeVisible({ timeout: 10_000 });

    // Deployments tab is active by default.
    await expect(
      page.getByRole("button", { name: "Deployments" }),
    ).toBeVisible();

    // Either shows deploy state or empty message.
    await expect(
      page
        .getByText("No deployments yet")
        .or(page.getByText("No deploy yet"))
        .or(page.getByRole("button", { name: "Redeploy" })),
    ).toBeVisible({ timeout: 5_000 });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });
});
