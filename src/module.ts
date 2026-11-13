import { DataSourcePlugin } from '@grafana/data';

import { ConfigEditor } from './components/ConfigEditor';
import { QueryEditor } from './components/QueryEditor';
import { QueryEditorHelp } from './components/QueryEditorHelp';
import { DataSource } from './datasource';
import { HardchargerOptions, HardchargerQuery, HardchargerSecureJsonData } from './types';

export const plugin = new DataSourcePlugin<DataSource, HardchargerQuery, HardchargerOptions, HardchargerSecureJsonData>(
  DataSource
)
  .setConfigEditor(ConfigEditor)
  .setQueryEditor(QueryEditor)
  .setQueryEditorHelp(QueryEditorHelp);
