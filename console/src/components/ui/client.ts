// The browser script of the console. Each interactive component exports
// mount(), and this entry calls them once. The layout loads it on every page.
// The big islands (document editor, Dev Tools, Watch) have their own entries.
import { mount as aggregateForm } from '@/components/aggregate-form';
import { mount as backupDownload } from '@/components/backup-download';
import { mount as connectionTest } from '@/components/connection-test';
import { mount as discover } from '@/components/discover';
import { mount as documentDrawer } from '@/components/document-drawer';
import { mount as explorerTools } from '@/components/explorer-tools';
import { mount as findForm } from '@/components/find-form';
import { mount as fold } from '@/components/fold';
import { mount as sideCollapse } from '@/components/side-collapse';
import { mount as insertForm } from '@/components/insert-form';

import { mount as alertDialog } from './alert-dialog';
import { mount as codeBlock } from './code-block';
import { mount as codeEditor } from './code-editor';
import { mount as combobox } from './combobox';
import { mount as dataTable } from './data-table';
import { mount as datePicker } from './date-picker';
import { mount as dialog } from './dialog';
import { mount as formValidation } from './form-validation';
import { mount as menu } from './menu';
import { mount as popover } from './popover';
import { mount as select } from './select';

export function mountAll(): void {
  for (const mount of [connectionTest, discover, dialog, alertDialog, popover, datePicker, codeBlock, insertForm, backupDownload, findForm, select, dataTable, fold, menu, explorerTools, combobox, aggregateForm, codeEditor, sideCollapse, formValidation, documentDrawer]) mount();
}

if (typeof document !== 'undefined') mountAll();
