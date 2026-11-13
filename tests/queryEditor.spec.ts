import { expect, PROVISIONED, setSql, test } from './fixtures';

test.describe('the query editor in a panel', () => {
  test('a query runs and its rows show in a table, a NULL staying empty', async ({
    panelEditPage,
    readProvisionedDataSource,
    page,
  }) => {
    const ds = await readProvisionedDataSource(PROVISIONED);
    await panelEditPage.datasource.set(ds.name);
    await panelEditPage.setVisualization('Table');
    const row = panelEditPage.getQueryEditorRow('A');
    await expect(row.getByLabel('Table', { exact: true })).toBeChecked();

    const response = panelEditPage.waitForQueryDataResponse();
    await setSql(
      page,
      row,
      "SELECT 42 AS answer, 'héllo' AS greeting, NULL::INTEGER AS nothing, [1, 2, 3] AS list, 12.5::DECIMAL(9,2) AS price"
    );
    await expect(response).toBeOK();
    await expect(panelEditPage.panel.fieldNames).toHaveText(['answer', 'greeting', 'nothing', 'list', 'price']);
    await expect(panelEditPage.panel.data).toContainText(['42', 'héllo', '[1,2,3]', '12.5']);
    await expect(panelEditPage.panel.getErrorIcon()).toHaveCount(0);
  });

  test('macros and the time series format make one series per label value', async ({
    panelEditPage,
    readProvisionedDataSource,
    page,
  }) => {
    const ds = await readProvisionedDataSource(PROVISIONED);
    await panelEditPage.datasource.set(ds.name);
    await panelEditPage.setVisualization('Table');
    const row = panelEditPage.getQueryEditorRow('A');
    await setSql(
      page,
      row,
      'SELECT $__timeGroup(ts, $__interval) AS time, host, avg(cpu_percent) AS cpu FROM demo.metrics ' +
        "WHERE $__timeFilter(ts) AND host IN ('web-1', 'db-1') GROUP BY 1, 2 ORDER BY 1"
    );
    // As a table: the long rows, with the host as a column.
    await expect(panelEditPage.panel.fieldNames).toHaveText(['time', 'host', 'cpu']);

    const response = panelEditPage.waitForQueryDataResponse();
    await row.getByLabel('Time series').check({ force: true });
    const answered = await response;
    expect(answered.ok()).toBe(true);
    // As time series: a wide frame, one cpu field per host, labelled with it.
    const frame = (await answered.json()).results.A.frames[0];
    expect(frame.schema.meta.type).toBe('timeseries-wide');
    expect(frame.schema.fields.map((field: { name: string; labels?: object }) => [field.name, field.labels])).toEqual([
      ['time', undefined],
      ['cpu', { host: 'db-1' }],
      ['cpu', { host: 'web-1' }],
    ]);
    // The SQL the server ran has the macros expanded, and the interval is Grafana's own.
    expect(frame.schema.meta.executedQueryString).toMatch(
      /^SELECT time_bucket\(INTERVAL '\d+ (seconds|milliseconds)', ts\) AS time, host, .* WHERE ts >= '\d{4}-\d\d-\d\dT[\d:.]+Z' AND ts <= '/
    );
    expect(frame.data.values[0].length).toBeGreaterThan(10);
    await expect(panelEditPage.panel.getErrorIcon()).toHaveCount(0);
  });

  test.describe('a query the server refuses', () => {
    // Grafana answers 400 for a failed query; the browser logs it.
    test.use({ expectedErrors: [/status of 400/] });

    test("shows the server's message on the panel", async ({ panelEditPage, readProvisionedDataSource, page }) => {
      const ds = await readProvisionedDataSource(PROVISIONED);
      await panelEditPage.datasource.set(ds.name);
      await panelEditPage.setVisualization('Table');
      const response = panelEditPage.waitForQueryDataResponse();
      await setSql(page, panelEditPage.getQueryEditorRow('A'), 'SELECT * FROM no_such_table');
      const answered = await response;
      expect(answered.status()).toBe(400);
      const result = (await answered.json()).results.A;
      expect(result.status).toBe(400);
      expect(result.errorSource).toBe('downstream');
      expect(result.error).toContain('Table with name no_such_table does not exist');
      await expect(panelEditPage.panel.getErrorIcon()).toBeVisible();
    });
  });
});

test('Explore runs a query', async ({ explorePage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource(PROVISIONED);
  await explorePage.datasource.set(ds.name);
  const response = explorePage.waitForQueryDataResponse();
  await setSql(page, explorePage.getQueryEditorRow('A'), "SELECT 'from explore' AS source, 7 * 6 AS answer");
  await expect(response).toBeOK();
  await expect(explorePage.tablePanel.fieldNames).toContainText(['source', 'answer']);
  await expect(explorePage.tablePanel.data).toContainText(['from explore', '42']);
});
