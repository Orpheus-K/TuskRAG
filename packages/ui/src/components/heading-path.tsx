import { ChevronRight } from "lucide-react";
import { Fragment } from "react";

export function HeadingPath({ parts }: { parts: readonly string[] }) {
  return parts.map((part, index) => (
    <Fragment key={index}>
      {index > 0 && (
        <>
          <ChevronRight
            aria-hidden="true"
            className="mx-0.5 inline size-3 align-middle"
          />
          <span className="sr-only"> / </span>
        </>
      )}
      {part}
    </Fragment>
  ));
}
