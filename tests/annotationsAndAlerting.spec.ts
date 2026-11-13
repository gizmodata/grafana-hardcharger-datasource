import { expect, PROVISIONED, setSql, test } from './fixtures';

test('an annotation query returns events', async ({ annotationEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource(PROVISIONED);
  // Every query the editor sends while the SQL is typed.
  const sent: string[] = [];
  page.on('request', (request) => {
    if (request.url().includes('/api/ds/query') && request.method() === 'POST') {
      sent.push(request.postDataJSON().queries?.[0]?.rawSql);
    }
  });
  await annotationEditPage.datasource.set(ds.name);
  await setSql(
    page,
    page.locator('body'),
    'SELECT ts AS time, message AS text, tags FROM demo.events WHERE $__timeFilter(ts)'
  );
  await expect(annotationEditPage.runQuery()).toBeOK();
  await expect(page.getByText(/\d+ events? \(from 3 fields\)/)).toBeVisible();
  // Grafana runs an annotation's query whenever it changes. The editor only changes it when the SQL
  // is committed, so no half-typed statement ever reached the server.
  expect(sent.length).toBeGreaterThan(0);
  expect(sent.filter((sql) => !sql.includes('AS time'))).toEqual([]);
  expect(sent.length).toBeLessThanOrEqual(4);
});

// Alert rules are evaluated by Grafana's backend alone: no browser expands variables or macros.
test('an alert rule evaluates a query', async ({ alertRuleEditPage, readProvisionedDataSource, page }) => {
  const ds = await readProvisionedDataSource(PROVISIONED);
  const queryA = await alertRuleEditPage.getQueryRow('A');
  await queryA.datasource.set(ds.name);
  await setSql(
    page,
    queryA.locator,
    'SELECT $__timeGroup(ts, $__interval) AS time, host, avg(cpu_percent) AS cpu FROM demo.metrics ' +
      'WHERE $__timeFilter(ts) GROUP BY 1, 2 ORDER BY 1'
  );
  await queryA.locator.getByLabel('Time series').check({ force: true });
  await expect(alertRuleEditPage.evaluate()).toBeOK();
});
