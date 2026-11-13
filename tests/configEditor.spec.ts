import { expect, HardchargerOptions, HardchargerSecureJsonData, PROVISIONED, test } from './fixtures';

const TYPE = 'gizmodata-hardcharger-datasource';

test.describe('adding a data source', () => {
  test('a new data source connects, and Save & test reports the server version', async ({
    createDataSourceConfigPage,
    readProvisionedDataSource,
    page,
  }) => {
    const provisioned = await readProvisionedDataSource<HardchargerOptions, HardchargerSecureJsonData>(PROVISIONED);
    const configPage = await createDataSourceConfigPage({ type: TYPE });

    // What a new data source starts with.
    await expect(page.getByLabel('Use TLS')).toBeChecked();
    await expect(page.getByLabel('Username and password')).toBeChecked();
    await expect(page.getByLabel('Tag queries')).toBeChecked();

    await page.getByLabel('Host', { exact: true }).fill(provisioned.jsonData.host ?? '');
    await page.getByLabel('Port', { exact: true }).fill(String(provisioned.jsonData.port));
    // The development server runs without TLS.
    await page.getByLabel('Use TLS').uncheck({ force: true });
    await expect(page.getByText('This connection is not encrypted')).toBeVisible();
    await page.getByLabel('Username', { exact: true }).fill(provisioned.jsonData.username ?? '');
    await page.getByLabel('Password', { exact: true }).fill(provisioned.secureJsonData?.password ?? '');

    await expect(configPage.saveAndTest()).toBeOK();
    await expect(configPage).toHaveAlert('success', {
      hasText: /Connected to Hardcharger \d+\.\d+\.\d+ \(DuckDB v\d+\.\d+\.\d+\)\. Catalog: memory, schema: main\./,
    });
  });

  test.describe('with a wrong password', () => {
    // The health check answers 400 for a data source that cannot connect; the browser logs it.
    test.use({ expectedErrors: [/status of 400/] });

    test('Save & test says the server refused the credentials, without repeating them', async ({
      createDataSourceConfigPage,
      readProvisionedDataSource,
      page,
    }) => {
      const provisioned = await readProvisionedDataSource<HardchargerOptions, HardchargerSecureJsonData>(PROVISIONED);
      const configPage = await createDataSourceConfigPage({ type: TYPE });
      await page.getByLabel('Host', { exact: true }).fill(provisioned.jsonData.host ?? '');
      await page.getByLabel('Port', { exact: true }).fill(String(provisioned.jsonData.port));
      await page.getByLabel('Use TLS').uncheck({ force: true });
      await page.getByLabel('Username', { exact: true }).fill(provisioned.jsonData.username ?? '');
      await page.getByLabel('Password', { exact: true }).fill('not-the-password-e2e');

      await expect(configPage.saveAndTest()).not.toBeOK();
      await expect(configPage).toHaveAlert('error', { hasText: 'The server refused the credentials' });
      await expect(page.getByText('not-the-password-e2e')).toHaveCount(0);
    });
  });

  test.describe('without a password', () => {
    test.use({ expectedErrors: [/status of 400/] });

    test('Save & test says what is missing', async ({ createDataSourceConfigPage, page }) => {
      const configPage = await createDataSourceConfigPage({ type: TYPE });
      await page.getByLabel('Host', { exact: true }).fill('hardcharger');
      await expect(configPage.saveAndTest()).not.toBeOK();
      await expect(configPage).toHaveAlert('error', { hasText: 'a username and a password are required' });
    });
  });
});

test.describe('editing a data source', () => {
  // The first Save & test below runs against a port nothing listens on.
  test.use({ expectedErrors: [/status of 400/] });

  test('everything set at creation can be changed afterwards', async ({
    createDataSource,
    gotoDataSourceConfigPage,
    readProvisionedDataSource,
    page,
  }) => {
    const provisioned = await readProvisionedDataSource<HardchargerOptions, HardchargerSecureJsonData>(PROVISIONED);
    // Created wrong in every field that can be wrong.
    const created = await createDataSource({
      type: TYPE,
      jsonData: {
        host: 'wrong-host.invalid',
        port: 1,
        useTLS: true,
        username: 'wrong-user',
        catalog: 'no_such_catalog',
        schema: 'no_such_schema',
        maxRows: 5,
        queryTimeout: 1,
        tagQueries: false,
        tlsSkipVerify: true,
        tlsServerName: 'wrong.invalid',
      },
      secureJsonData: { password: 'wrong-password' },
    });
    const configPage = await gotoDataSourceConfigPage(created.uid);

    // The saved values are shown; the password only as "configured".
    await expect(page.getByLabel('Host', { exact: true })).toHaveValue('wrong-host.invalid');
    await expect(page.getByLabel('Port', { exact: true })).toHaveValue('1');
    await expect(page.getByLabel('Default catalog')).toHaveValue('no_such_catalog');
    await expect(page.getByLabel('Max rows')).toHaveValue('5');
    await expect(page.getByLabel('Tag queries')).not.toBeChecked();
    await expect(page.getByLabel('Password', { exact: true })).toBeDisabled();
    await expect(page.getByLabel('Password', { exact: true })).toHaveValue('configured');
    await expect(configPage.saveAndTest()).not.toBeOK();

    // Change every one of them.
    await page.getByLabel('TLS server name').fill('');
    await page.getByLabel('Skip TLS verification').uncheck({ force: true });
    await page.getByLabel('Host', { exact: true }).fill(provisioned.jsonData.host ?? '');
    await page.getByLabel('Port', { exact: true }).fill(String(provisioned.jsonData.port));
    await page.getByLabel('Use TLS').uncheck({ force: true });
    await page.getByLabel('Username', { exact: true }).fill(provisioned.jsonData.username ?? '');
    await page.getByRole('button', { name: 'Reset' }).click();
    await page.getByLabel('Password', { exact: true }).fill(provisioned.secureJsonData?.password ?? '');
    await page.getByLabel('Default catalog').fill('');
    await page.getByLabel('Default schema').fill('demo');
    await page.getByLabel('Max rows').fill('');
    await page.getByLabel('Query timeout (seconds)').fill('30');
    await page.getByLabel('Tag queries').check({ force: true });

    await expect(configPage.saveAndTest()).toBeOK();
    await expect(configPage).toHaveAlert('success', { hasText: 'Catalog: memory, schema: demo.' });

    // And they are what is saved.
    await page.reload();
    await expect(page.getByLabel('Host', { exact: true })).toHaveValue(provisioned.jsonData.host ?? '');
    await expect(page.getByLabel('Default schema')).toHaveValue('demo');
    await expect(page.getByLabel('Default catalog')).toHaveValue('');
    await expect(page.getByLabel('Max rows')).toHaveValue('');
    await expect(page.getByLabel('Query timeout (seconds)')).toHaveValue('30');
    await expect(page.getByLabel('Use TLS')).not.toBeChecked();
    await expect(page.getByLabel('Tag queries')).toBeChecked();
  });
});

test('the provisioned data source is healthy', async ({ gotoDataSourceConfigPage, readProvisionedDataSource }) => {
  const provisioned = await readProvisionedDataSource(PROVISIONED);
  const configPage = await gotoDataSourceConfigPage(provisioned.uid);
  await expect(configPage.saveAndTest()).toBeOK();
  await expect(configPage).toHaveAlert('success', { hasText: 'Connected to Hardcharger' });
});
