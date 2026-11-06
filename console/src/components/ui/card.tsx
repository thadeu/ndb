import type { Child, FC } from 'hono/jsx';

export const Card: FC<{ class?: string; href?: string; children?: Child }> = ({ class: extra, href, children }) => {
  const className = extra ? `card ${extra}` : 'card';

  if (href) {
    return (
      <a class={`${className} link`} href={href}>
        {children}
      </a>
    );
  }

  return <section class={className}>{children}</section>;
};

export const CardHeader: FC<{ title: string; description?: string; action?: Child }> = ({ title, description, action }) => (
  <div class="card-head">
    <div>
      <h2 class="card-title">{title}</h2>
      {description && <p class="card-desc">{description}</p>}
    </div>
    {action}
  </div>
);
