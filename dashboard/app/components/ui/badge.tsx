import type { HTMLAttributes } from "react";
import { cn } from "~/lib/cn";

export function Badge({ className, variant = "secondary", ...props }: HTMLAttributes<HTMLSpanElement> & { variant?: "default" | "secondary" | "success" | "warning" | "danger" | "outline" }) {
  return <span className={cn("inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium", variant === "default" && "border-transparent bg-primary text-primary-foreground", variant === "secondary" && "bg-secondary text-secondary-foreground", variant === "success" && "border-transparent bg-[color:var(--status-success)]/15 text-[color:var(--status-success)]", variant === "warning" && "border-transparent bg-[color:var(--status-warning)]/15 text-[color:var(--status-warning)]", variant === "danger" && "border-transparent bg-[color:var(--status-danger)]/15 text-[color:var(--status-danger)]", variant === "outline" && "text-foreground", className)} {...props} />;
}
