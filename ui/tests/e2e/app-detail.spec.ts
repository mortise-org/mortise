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

// End-to-end tests for the App detail page (/projects/{project}/apps/{app}).
//
// Assumes an operator is reachable at MORTISE_BASE_URL and admin credentials
// are supplied via MORTISE_ADMIN_EMAIL / MORTISE_ADMIN_PASSWORD.

test.describe("app detail page", () => {
  let adminToken: string;
  const projectName = `e2e-appdet-${randomSuffix()}`;

  test.beforeAll(async ({ request }) => {
    await ensureAdmin(request);
    adminToken = await loginViaAPI(request);
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

  test("page structure - breadcrumbs, heading, phase badge, overview cards, all sections", async ({
    page,
    request,
  }) => {
    const appName = `e2e-struct-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);

    // Header: breadcrumbs
    await expect(page.getByRole("link", { name: "Projects" })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole("link", { name: projectName })).toBeVisible();
    await expect(page.getByText("apps").first()).toBeVisible();

    // Heading with app name
    await expect(page.getByRole("heading", { name: appName })).toBeVisible();

    // Phase badge (should be one of the known phases)
    await expect(
      page.getByText(/Pending|Building|Deploying|Ready|Failed/).first(),
    ).toBeVisible();

    // Delete App button
    await expect(
      page.getByRole("button", { name: "Delete App" }),
    ).toBeVisible();

    // Overview cards
    await expect(page.getByText("Source")).toBeVisible();
    await expect(page.getByText("Container Image")).toBeVisible();
    await expect(page.getByText("Replicas")).toBeVisible();
    await expect(page.getByText("Domain")).toBeVisible();

    // Deploy section
    await expect(page.getByText("Deploy new image")).toBeVisible();
    await expect(page.getByRole("button", { name: "Deploy" })).toBeVisible();

    // Environment Variables section
    await expect(page.getByText("Environment Variables")).toBeVisible();

    // Logs section
    await expect(page.getByText("Logs", { exact: true })).toBeVisible();

    // Domains section
    await expect(page.getByText("Domains", { exact: true })).toBeVisible();

    // Secrets section
    await expect(page.getByText("Secrets", { exact: true })).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("deploy new image - fill input, click Deploy, verify loading state", async ({
    page,
    request,
  }) => {
    const appName = `e2e-deploy-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Intercept the deploy API call so we can control timing and avoid errors.
    await page.route(
      `**/api/projects/*/apps/*/deploy`,
      (route) =>
        // Hold the request open briefly so we can observe the loading state.
        new Promise((resolve) => {
          setTimeout(() => {
            resolve(
              route.fulfill({
                status: 200,
                contentType: "application/json",
                body: JSON.stringify({
                  status: "ok",
                  app: appName,
                  image: "nginx:1.28",
                }),
              }),
            );
          }, 500);
        }),
    );

    const imageInput = page.getByPlaceholder("registry.example.com/app:v2.0.0");
    await imageInput.fill("nginx:1.28");

    const deployButton = page.getByRole("button", { name: "Deploy" });
    await deployButton.click();

    // Should show "Deploying..." while the request is in-flight.
    await expect(
      page.getByRole("button", { name: "Deploying..." }),
    ).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("deploy button disabled when image input is empty", async ({
    page,
    request,
  }) => {
    const appName = `e2e-disbtn-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    const deployButton = page.getByRole("button", { name: "Deploy" });
    await expect(deployButton).toBeDisabled();

    // Fill the input, button should become enabled.
    const imageInput = page.getByPlaceholder("registry.example.com/app:v2.0.0");
    await imageInput.fill("nginx:1.28");
    await expect(deployButton).toBeEnabled();

    // Clear the input, button should be disabled again.
    await imageInput.fill("");
    await expect(deployButton).toBeDisabled();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("environment variables - form mode: add and remove variable", async ({
    page,
    request,
  }) => {
    const appName = `e2e-envform-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Should start in Form mode by default.
    await expect(page.getByText("No variables yet")).toBeVisible();

    // Click "+ Add variable"
    await page.getByRole("button", { name: "+ Add variable" }).click();

    // KEY and value inputs should appear.
    const keyInput = page.getByPlaceholder("KEY");
    const valueInput = page.getByPlaceholder("value");
    await expect(keyInput).toBeVisible();
    await expect(valueInput).toBeVisible();

    // Fill them in.
    await keyInput.fill("MY_VAR");
    await valueInput.fill("my_value");

    // Remove the variable.
    await page.getByRole("button", { name: "Remove variable" }).click();

    // "No variables yet" text should reappear.
    await expect(page.getByText("No variables yet")).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("environment variables - raw mode: textarea appears with placeholder", async ({
    page,
    request,
  }) => {
    const appName = `e2e-envraw-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Click "Raw" toggle to switch modes.
    await page.getByRole("button", { name: "Raw" }).click();

    // Textarea should appear with the expected placeholder.
    const textarea = page.getByPlaceholder("KEY=value");
    await expect(textarea).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("environment variables - mode switch preserves data", async ({
    page,
    request,
  }) => {
    const appName = `e2e-envswitch-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Add a variable in form mode.
    await page.getByRole("button", { name: "+ Add variable" }).click();
    await page.getByPlaceholder("KEY").fill("FOO");
    await page.getByPlaceholder("value").fill("bar");

    // Switch to Raw mode.
    await page.getByRole("button", { name: "Raw" }).click();

    // Textarea should contain the variable in KEY=value format.
    const textarea = page.getByPlaceholder("KEY=value");
    await expect(textarea).toHaveValue(/FOO=bar/);

    // Switch back to Form mode.
    await page.getByRole("button", { name: "Form" }).click();

    // KEY/value pair should be restored.
    await expect(page.locator('input[value="FOO"]')).toBeVisible();
    await expect(page.locator('input[value="bar"]')).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("environment variables - save and discard buttons appear when dirty", async ({
    page,
    request,
  }) => {
    const appName = `e2e-envdirty-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Initially no Save/Discard buttons.
    await expect(
      page.getByRole("button", { name: "Save changes" }),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Discard" })).toHaveCount(0);

    // Add a variable to make it dirty.
    await page.getByRole("button", { name: "+ Add variable" }).click();
    await page.getByPlaceholder("KEY").fill("DIRTY_VAR");
    await page.getByPlaceholder("value").fill("dirty_value");

    // Save changes and Discard buttons should appear.
    await expect(
      page.getByRole("button", { name: "Save changes" }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Discard" })).toBeVisible();

    // Click Discard.
    await page.getByRole("button", { name: "Discard" }).click();

    // Buttons should disappear.
    await expect(
      page.getByRole("button", { name: "Save changes" }),
    ).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Discard" })).toHaveCount(0);

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("secrets CRUD - add and remove a secret", async ({ page, request }) => {
    const appName = `e2e-secret-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Intercept the secret creation API.
    const secretName = `E2E_SECRET_${randomSuffix().toUpperCase()}`;
    await page.route(`**/api/projects/*/apps/*/secrets`, (route) => {
      if (route.request().method() === "POST") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ name: secretName, keys: [secretName] }),
        });
      }
      // GET (list) returns the newly created secret.
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([{ name: secretName, keys: [secretName] }]),
      });
    });

    // Fill the secret form.
    await page.getByPlaceholder("SECRET_NAME").fill(secretName);
    await page.getByPlaceholder("value (write-only)").fill("s3cr3t");

    // Click Add.
    await page.getByRole("button", { name: "Add" }).click();

    // Secret should appear in the list.
    await expect(page.getByText(secretName).first()).toBeVisible({
      timeout: 5_000,
    });
    await expect(page.getByText("1 key")).toBeVisible();

    // Intercept the delete and subsequent list call.
    await page.route(
      `**/api/projects/*/apps/*/secrets/${secretName}`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ status: "deleted" }),
        }),
    );
    // After deletion, the list should return empty.
    await page.unroute(`**/api/projects/*/apps/*/secrets`);
    await page.route(`**/api/projects/*/apps/*/secrets`, (route) => {
      if (route.request().method() === "GET") {
        return route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify([]),
        });
      }
      return route.continue();
    });

    // Click Remove on the secret.
    await page.getByRole("button", { name: "Remove" }).click();

    // Secret should no longer be in the list.
    await expect(page.getByText(secretName)).toHaveCount(0, { timeout: 5_000 });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("domains - add custom domain", async ({ page, request }) => {
    const appName = `e2e-domain-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

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

    // Find the Domains section and fill the input.
    const domainInput = page.getByPlaceholder("custom.example.com");
    await domainInput.fill(customDomain);

    // The Add button in the domains section.
    const addButton = domainInput
      .locator("..")
      .getByRole("button", { name: "Add" });
    await expect(addButton).toBeEnabled();
    await addButton.click();

    // The custom domain should appear in the list.
    await expect(page.getByText(customDomain)).toBeVisible({ timeout: 5_000 });

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("logs section renders with controls", async ({ page, request }) => {
    const appName = `e2e-logs-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Logs heading
    await expect(page.getByText("Logs", { exact: true })).toBeVisible();

    // Pod filter dropdown (defaults to "All pods")
    await expect(page.getByText("All pods")).toBeVisible();

    // Control buttons
    await expect(page.getByRole("button", { name: "Pause" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Clear" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Download" })).toBeVisible();

    // Line count display
    await expect(page.getByText(/\d+ lines?/)).toBeVisible();

    await deleteAppViaAPI(request, adminToken, projectName, appName);
  });

  test("delete app flow - confirm dialog, redirect to project page", async ({
    page,
    request,
  }) => {
    const appName = `e2e-delappp-${randomSuffix()}`;
    await createAppViaAPI(request, adminToken, projectName, appName);

    await injectToken(page, adminToken);
    await page.goto(`/projects/${projectName}/apps/${appName}`);
    await expect(page.getByRole("heading", { name: appName })).toBeVisible({
      timeout: 10_000,
    });

    // Set up dialog handler to accept the confirmation.
    page.once("dialog", async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain(appName);
      await dialog.accept();
    });

    await page.getByRole("button", { name: "Delete App" }).click();

    // Should redirect to the project page.
    await expect(page).toHaveURL(
      `/projects/${encodeURIComponent(projectName)}`,
      {
        timeout: 10_000,
      },
    );
  });
});
