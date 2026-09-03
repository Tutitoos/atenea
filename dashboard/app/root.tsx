import {
  isRouteErrorResponse,
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
} from "react-router";
import type { Route } from "./+types/root";
import "./styles/tailwind.css";

export function Layout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="es">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="color-scheme" content="dark light" />
        <meta name="theme-color" content="#0b1018" />
        <Meta />
        <Links />
      </head>
      <body>
        {children}
        <ScrollRestoration nonce="__ATENEA_CSP_NONCE__" />
        <Scripts nonce="__ATENEA_CSP_NONCE__" />
      </body>
    </html>
  );
}

export default function App() {
  return <Outlet />;
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  const message = isRouteErrorResponse(error)
    ? `${error.status} · ${error.statusText}`
    : "No se pudo cargar esta vista";
  return (
    <main className="grid min-h-svh place-items-center bg-background p-6 text-foreground">
      <section className="w-full max-w-lg rounded-xl border bg-card p-8 shadow-sm">
        <p className="mb-2 text-xs font-semibold uppercase tracking-[0.18em] text-muted-foreground">
          Atenea · observabilidad
        </p>
        <h1 className="text-2xl font-semibold">{message}</h1>
        <p className="mt-3 text-sm text-muted-foreground">
          Vuelve a intentarlo o regresa a la vista general.
        </p>
        <a className="mt-6 inline-flex items-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground" href="/">
          Ir a Overview
        </a>
      </section>
    </main>
  );
}
