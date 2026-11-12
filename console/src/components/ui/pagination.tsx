import type { Child, FC } from 'hono/jsx';

import { Button } from './button';
import { Icon } from './icon';

export interface PaginationProps {
  page: number;
  from: number;
  to: number;
  total?: number;
  firstHref?: string;
  prevHref?: string;
  nextHref?: string;
  children?: Child;
}

// Pagination is the footer of a list: the range on this page, the page
// number, and the links to the first, the previous and the next page. A
// link that is not given shows as a disabled button. Children go between
// the range and the links, such as a page size control.
export const Pagination: FC<PaginationProps> = ({ page, from, to, total, firstHref, prevHref, nextHref, children }) => {
  const range = to === 0 ? 'No rows' : `${from.toLocaleString('en-US')}–${to.toLocaleString('en-US')}`;
  const of = total === undefined || to === 0 ? '' : ` of ${total.toLocaleString('en-US')}`;

  return (
    <nav class="pagination" aria-label="Pages">
      <span class="pagination-range">
        {range}
        {of}
      </span>

      {children}

      <span class="grow" />

      <div class="pagination-links">
        <PageLink href={firstHref} label="First page">
          <Icon name="chevrons-left" />
        </PageLink>

        <PageLink href={prevHref} label="Previous page">
          <Icon name="chevron-left" />
        </PageLink>

        <span class="pagination-page">Page {page}</span>

        <PageLink href={nextHref} label="Next page">
          <Icon name="chevron-right" />
        </PageLink>
      </div>
    </nav>
  );
};

const PageLink: FC<{ href?: string; label: string; children?: Child }> = ({ href, label, children }) => {
  if (!href) {
    return (
      <Button size="sm" class="icon-only" disabled aria-label={label}>
        {children}
      </Button>
    );
  }

  return (
    <Button size="sm" class="icon-only" href={href} aria-label={label}>
      {children}
    </Button>
  );
};
