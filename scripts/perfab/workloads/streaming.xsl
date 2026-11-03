<!-- streaming: xsl:source-document streamable="yes" over the 37 MB document (parameter src), bounded memory -->
<xsl:stylesheet version="3.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:map="http://www.w3.org/2005/xpath-functions/map" xmlns:array="http://www.w3.org/2005/xpath-functions/array" xmlns:f="urn:f" exclude-result-prefixes="xs map array f">
  <xsl:output method="xml" indent="no"/>
  <xsl:param name="src" as="xs:string"/>
  <xsl:template name="xsl:initial-template">
    <xsl:source-document href="{$src}" streamable="yes">
      <summary>
        <xsl:for-each select="orders/order">
          <o id="{@id}" customer="{customer}"/>
        </xsl:for-each>
      </summary>
    </xsl:source-document>
  </xsl:template>
</xsl:stylesheet>
