// The PITR restore parts that the Backups page and the Backups tab of a
// collection share: the window bar, the fields of the restore form, and the
// confirm step. The form is a GET with confirm=1, so the server renders the
// confirm step and it works without the script.
import type { FC } from 'hono/jsx';

import type { PitrInfo } from '@/ndb';
import { barLeft, defaultAt, seconds, shortWhen, sliderRange, utc, when } from '@/pitr';
import { Button } from '@/ui/button';
import { DatePicker } from '@/ui/date-picker';
import { Dialog } from '@/ui/dialog';
import { Field } from '@/ui/field';
import { Input } from '@/ui/input';

export type RestoreWhere = 'copy' | 'place';

// RestoreDraft is the state of the restore form in the URL.
export interface RestoreDraft {
  at?: string;
  where?: RestoreWhere;
  to?: string;
  confirm?: boolean;
}

// RestoreMode says what the confirmed restore does: a copy only, a copy that
// replaces the collection at once, or a copy with the rename as a second step.
export type RestoreMode = 'copy' | 'replace' | 'steps';

const NAME_PATTERN = '[a-z0-9][a-z0-9_\\-]{0,63}';

const PITR_PRESETS = [
  { label: '5 minutes ago', ago: '5m' },
  { label: '15 minutes ago', ago: '15m' },
  { label: '1 hour ago', ago: '1h' },
  { label: '6 hours ago', ago: '6h' },
  { label: 'Start of the window', at: 'min' as const },
];

export function copyName(collection: string, draft: RestoreDraft = {}): string {
  return draft.to?.trim() || `${collection}_restored`;
}

export const WindowBar: FC<{ item: PitrInfo }> = ({ item }) => {
  const fill = `left:min(${barLeft(item.window, item.oldest)}%, calc(100% - 8px))`;
  const label = item.oldest ? `Restore window of ${item.window}: from ${when(item.oldest)} to now` : `Restore window of ${item.window}: nothing to restore yet`;

  return (
    <span class="pitr-window">
      <span class="pitr-bar" role="img" aria-label={label}>
        {item.oldest && <span class="pitr-fill" style={fill} />}
      </span>

      <span class="pitr-ends">
        <span>{shortWhen(item.oldest)}</span>
        <span>now</span>
      </span>
    </span>
  );
};

// RestoreFields is the body of the restore form: the point in time (date
// picker and slider) and where the restore goes.
export const RestoreFields: FC<{ item: PitrInfo; draft: RestoreDraft; placeHint: string }> = ({ item, draft, placeHint }) => {
  const at = draft.at ?? defaultAt(item.oldest);
  const range = item.oldest ? sliderRange(item.oldest) : undefined;
  const inPlace = draft.where === 'place';

  return (
    <>
      <Field id="pitr-at" label="Point in time (UTC)">
        <DatePicker
          id="pitr-at"
          name="at"
          label="Point in time (UTC)"
          value={at}
          min={item.oldest}
          max="now"
          presets={PITR_PRESETS}
          required
        />
      </Field>

      {range && (
        <div class="pitr-slide">
          <input
            class="pitr-slider"
            type="range"
            min={range.min}
            max={range.max}
            step={1}
            value={seconds(at) ?? range.max}
            aria-label="Move the point in time"
            data-pitr-slider="pitr-at"
            hidden
          />

          <span class="pitr-ends">
            <span>{shortWhen(item.oldest)}</span>
            <span>now</span>
          </span>
        </div>
      )}

      <fieldset class="choices">
        <legend class="label">Where</legend>

        <label class="choice">
          <input type="radio" name="where" value="copy" checked={!inPlace} />

          <span class="choice-text">
            <strong>As a copy</strong>

            <Input
              id="pitr-to"
              mono
              name="to"
              value={copyName(item.collection, draft)}
              pattern={NAME_PATTERN}
              aria-label="Name of the copy"
              required
            />

            <span class="hint">Compare first. The live collection does not change.</span>
          </span>
        </label>

        <label class="choice">
          <input type="radio" name="where" value="place" checked={inPlace} />

          <span class="choice-text">
            <strong>In place</strong>
            <span class="hint">{placeHint}</span>
          </span>
        </label>
      </fieldset>
    </>
  );
};

export interface RestoreConfirmProps {
  collection: string;
  at: string;
  to: string;
  mode: RestoreMode;
  action: string;
  cancelHref: string;
  back?: 'explorer';
}

const TITLES: Record<RestoreMode, (collection: string) => string> = {
  copy: collection => `Restore a copy of ${collection}?`,
  replace: collection => `Replace ${collection} with a restore?`,
  steps: collection => `Restore ${collection}, step 1 of 2?`,
};

const ACTIONS: Record<RestoreMode, (collection: string) => string> = {
  copy: () => 'Restore a copy',
  replace: collection => `Replace ${collection}`,
  steps: () => 'Make the copy',
};

// RestoreConfirm is the confirm step of a restore: a dialog that the server
// renders open. It states the collection, the instant and the place, and its
// form is the POST that does the restore.
export const RestoreConfirm: FC<RestoreConfirmProps> = ({ collection, at, to, mode, action, cancelHref, back }) => {
  const instant = utc(at);
  const text = at.trim().replace('T', ' ').slice(0, 19);
  const variant = mode === 'replace' ? 'destructive' : 'dark';

  return (
    <Dialog
      id="restore-confirm"
      class="restore-confirm"
      title={TITLES[mode](collection)}
      open
      closeHref={cancelHref}
    >
      <dl class="restore-facts">
        <div>
          <dt>Collection</dt>
          <dd class="mono">{collection}</dd>
        </div>

        <div>
          <dt>Point in time</dt>

          <dd>
            <span class="mono">{text} UTC</span>
            <time class="restore-local" datetime={instant} data-local-time />
          </dd>
        </div>

        <div>
          <dt>Goes to</dt>

          {mode === 'replace' ? (
            <dd>
              <span class="mono">{collection}</span>, in place
            </dd>
          ) : (
            <dd>
              a new collection, <span class="mono">{to}</span>
            </dd>
          )}
        </div>
      </dl>

      {mode === 'copy' && <p class="hint">{collection} does not change. Writes after this instant are not in the copy.</p>}

      {mode === 'steps' && (
        <p class="hint">
          This step only makes the copy. {collection} does not change until you confirm the rename in step 2.
        </p>
      )}

      {mode === 'replace' && (
        <p class="note bad restore-danger">
          This replaces the current {collection}. Every write after this instant goes away from it. ndb moves the current {collection} to the trash, where it stays restorable for its PITR window. The watch consumers of {collection} start over.
        </p>
      )}

      <form class="restore-confirm-actions" method="post" action={action}>
        <input type="hidden" name="collection" value={collection} />
        <input type="hidden" name="at" value={at} />
        <input type="hidden" name="to" value={to} />
        <input type="hidden" name="where" value={mode === 'copy' ? 'copy' : 'place'} />

        {mode === 'replace' && <input type="hidden" name="replace" value="1" />}
        {back && <input type="hidden" name="back" value={back} />}

        <Button variant="ghost" href={cancelHref} data-dialog-close>
          Cancel
        </Button>

        <Button variant={variant} type="submit">
          {ACTIONS[mode](collection)}
        </Button>
      </form>
    </Dialog>
  );
};
