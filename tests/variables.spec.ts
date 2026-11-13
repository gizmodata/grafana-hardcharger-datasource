import { expect, PROVISIONED, setSql, test } from './fixtures';

test.describe('query variables', () => {
  test("a variable's SQL gives its options", async ({ variableEditPage, readProvisionedDataSource, page }) => {
    const ds = await readProvisionedDataSource(PROVISIONED);
    await variableEditPage.setVariableType('Query');
    await variableEditPage.datasource.set(ds.name);
    await setSql(page, page.locator('body'), 'SELECT DISTINCT host FROM demo.metrics ORDER BY host');
    await variableEditPage.runQuery();
    await expect(variableEditPage).toDisplayPreviews(['db-1', 'db-2', 'web-1', 'web-2']);
  });

  test('__text and __value columns give labels that differ from the values', async ({
    variableEditPage,
    readProvisionedDataSource,
    page,
  }) => {
    const ds = await readProvisionedDataSource(PROVISIONED);
    await variableEditPage.setVariableType('Query');
    await variableEditPage.datasource.set(ds.name);
    await setSql(
      page,
      page.locator('body'),
      "SELECT DISTINCT upper(region) AS __text, region AS __value FROM demo.metrics ORDER BY 1"
    );
    await variableEditPage.runQuery();
    await expect(variableEditPage).toDisplayPreviews(['EU-WEST', 'US-EAST']);
  });
});

test.describe('variables in a dashboard', () => {
  test('the selected values are written into the SQL as a list of literals', async ({
    gotoDashboardPage,
    readProvisionedDashboard,
    page,
  }) => {
    const dashboard = await readProvisionedDashboard({ fileName: 'hardcharger-overview.json' });
    // Two of the four hosts, one of them with a name only a quoted literal can carry.
    const queries: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/api/ds/query') && request.method() === 'POST') {
        for (const query of request.postDataJSON().queries ?? []) {
          queries.push(query.rawSql);
        }
      }
    });
    const dashboardPage = await gotoDashboardPage({
      uid: dashboard.uid,
      queryParams: new URLSearchParams([
        ['var-host', 'web-1'],
        ['var-host', "db'1"],
      ]),
    });
    const table = dashboardPage.getPanelByTitle('Latest reading per host');
    await expect(table.data).toContainText(['web-1']);
    await expect(table.data).not.toContainText(['web-2']);
    await expect(table.getErrorIcon()).toHaveCount(0);

    const latest = queries.find((sql) => sql.includes('arg_max(cpu_percent, ts)'));
    expect(latest).toContain("host IN ('web-1','db''1')");
    // Grafana's own interval stays unquoted for $__timeGroup.
    const cpu = queries.find((sql) => sql.includes('avg(cpu_percent) AS cpu'));
    expect(cpu).toMatch(/\$__timeGroup\(ts, \d+(ms|s|m|h|d)\) AS time/);
  });
});
