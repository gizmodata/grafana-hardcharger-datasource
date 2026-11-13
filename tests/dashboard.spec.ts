import { expect, test } from './fixtures';

const PANELS = [
  'CPU by host',
  'Requests',
  'Average CPU',
  'Requests by region',
  'Latest reading per host',
  'DuckDB types as Grafana shows them',
];

// The browserErrors fixture fails this test on any console error, uncaught exception or 5xx answer.
test('the provisioned dashboard renders every panel without an error', async ({
  gotoDashboardPage,
  readProvisionedDashboard,
  page,
}) => {
  const dashboard = await readProvisionedDashboard({ fileName: 'hardcharger-overview.json' });
  expect(dashboard.title).toBe('Hardcharger overview');

  // Every query the dashboard sends, and how Grafana answered it.
  const answers: Array<{ status: number; refIds: string[]; errors: string[] }> = [];
  page.on('response', async (response) => {
    if (!response.url().includes('/api/ds/query')) {
      return;
    }
    const body = await response.json().catch(() => ({ results: {} }));
    const results = body.results ?? {};
    answers.push({
      status: response.status(),
      refIds: Object.keys(results),
      errors: Object.values(results)
        .map((result) => (result as { error?: string }).error)
        .filter((error): error is string => Boolean(error)),
    });
  });

  const dashboardPage = await gotoDashboardPage({ uid: dashboard.uid });
  for (const title of PANELS) {
    const panel = dashboardPage.getPanelByTitle(title);
    await panel.locator.scrollIntoViewIfNeeded();
    await expect(panel.locator, title).toBeVisible();
    await expect(panel.getErrorIcon(), title).toHaveCount(0);
    await expect(panel.locator.getByText('No data'), title).toHaveCount(0);
  }

  // The tables show the demo data and DuckDB's types.
  const latest = dashboardPage.getPanelByTitle('Latest reading per host');
  await expect(latest.fieldNames).toContainText(['host', 'region', 'latest', 'cpu_percent']);
  await expect(latest.data).toContainText(['db-1', 'db-2', 'web-1', 'web-2']);
  const types = dashboardPage.getPanelByTitle('DuckDB types as Grafana shows them');
  await expect(types.data).toContainText(['42', '3.14', '1 day 02:00:00', '[1,2,3]', '{"city":"Chicago","zip":60601}']);

  // The variable was filled from the server.
  await expect(page.getByTestId(/Dashboard template variables.*Host|template variable.*host/i).first()).toBeVisible();

  // Six panels, one annotation query and the variable query: all answered, none with an error.
  await expect.poll(() => answers.length, { message: 'queries answered' }).toBeGreaterThanOrEqual(PANELS.length + 1);
  expect(answers.filter((answer) => answer.status !== 200)).toEqual([]);
  expect(answers.flatMap((answer) => answer.errors)).toEqual([]);
});
