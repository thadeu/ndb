// The browser script of the console. Each interactive component exports
// mount(), and this entry calls them once. The layout loads it on every page.
// The big islands (document editor, Dev Tools, Watch) have their own entries.
import { mount as backupDownload } from '@/components/backup-download';
import { mount as connectionTest } from '@/components/connection-test';
import { mount as discover } from '@/components/discover';
import { mount as insertForm } from '@/components/insert-form';

import { mount as alertDialog } from './alert-dialog';
import { mount as codeBlock } from './code-block';
import { mount as dialog } from './dialog';

export function mountAll(): void {
  for (const mount of [connectionTest, discover, dialog, alertDialog, codeBlock, insertForm, backupDownload]) mount();
}

if (typeof document !== 'undefined') mountAll();
